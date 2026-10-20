package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/agent"
	"github.com/cowhorse05/KubeNode-Observer/internal/cpu"
	eventstore "github.com/cowhorse05/KubeNode-Observer/internal/event"
	"github.com/cowhorse05/KubeNode-Observer/internal/metrics"
	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/runqlat"
	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

func main() {
	listenAddress := flag.String("listen", ":18080", "health API listen address")
	debugAddress := flag.String("debug-listen", "", "opt-in loopback-only diagnostics and pprof address (e.g. 127.0.0.1:18090)")
	runtimeEndpoint := flag.String("runtime-endpoint", "unix:///run/containerd/containerd.sock", "CRI runtime endpoint")
	hostProc := flag.String("host-proc", "/host/proc", "host procfs mount")
	cgroupRoot := flag.String("cgroup-root", "/host/sys/fs/cgroup", "host cgroup v2 mount")
	topologyInterval := flag.Duration("topology-interval", 10*time.Second, "topology refresh interval")
	cpuInterval := flag.Duration("cpu-interval", time.Second, "CPU runtime sampling interval")
	nodeName := flag.String("node-name", os.Getenv("NODE_NAME"), "Kubernetes node name")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if *debugAddress != "" {
		host, _, err := net.SplitHostPort(*debugAddress)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			logger.Error("debug-listen must use a literal loopback IP and port")
			os.Exit(2)
		}
	}
	var timing *telemetry.Recorder
	if *debugAddress != "" {
		timing = &telemetry.Recorder{}
	}
	if *topologyInterval <= 0 || *cpuInterval <= 0 {
		logger.Error("collection intervals must be positive", "topologyInterval", *topologyInterval, "cpuInterval", *cpuInterval)
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	execProbe, err := probe.NewExecProbe()
	if err != nil {
		logger.Error("failed to initialize eBPF probe", "error", err)
		os.Exit(1)
	}
	defer execProbe.Close()
	cpuProbe, err := probe.NewCPUProbe()
	if err != nil {
		logger.Error("failed to initialize CPU eBPF probe", "error", err)
		os.Exit(1)
	}
	defer cpuProbe.Close()
	runQLatProbe, err := probe.NewRunQLatProbe()
	if err != nil {
		logger.Error("failed to initialize run queue latency eBPF probe", "error", err)
		os.Exit(1)
	}
	defer runQLatProbe.Close()

	criClient, err := topology.NewCRIClient(*runtimeEndpoint)
	if err != nil {
		logger.Error("failed to initialize CRI client", "error", err)
		os.Exit(1)
	}
	defer criClient.Close()

	var resolver topology.ResourceResolver
	kubernetesCache, err := topology.NewKubernetesCache(*nodeName)
	if err != nil {
		logger.Warn("Kubernetes informer is disabled", "error", err)
	} else {
		resolver = kubernetesCache
		go func() {
			if synced := kubernetesCache.Run(ctx); !synced && ctx.Err() == nil {
				logger.Warn("Kubernetes informer cache synchronization failed")
			}
		}()
	}

	topologyCollector := topology.NewCollector(*nodeName, criClient, topology.NewProcFS(*hostProc, *cgroupRoot), resolver)
	topologyCollector.Timing = timing
	topologyStore := topology.NewStore(topologyCollector)
	topologyStore.Timing = timing
	go topologyStore.Run(ctx, *topologyInterval)
	events := eventstore.NewStore(4096, topologyStore)
	cpuCollector := cpu.NewCollector(cpu.TimedSource{Source: cpuProbe, Recorder: timing, Operation: telemetry.CPUScan}, topologyStore)
	cpuCollector.Timing = timing
	go cpuCollector.Run(ctx, *cpuInterval)
	runQLatHandler := runqlat.NewHandler(runQLatProbe, topologyStore)

	refreshRequests := make(chan struct{}, 1)
	var unresolvedMu sync.Mutex
	unresolvedUntil := make(map[uint64]time.Time)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-refreshRequests:
				refreshContext, refreshCancel := context.WithTimeout(ctx, 3*time.Second)
				if err := topologyStore.Refresh(refreshContext); err != nil && ctx.Err() == nil {
					logger.Debug("on-demand topology refresh failed", "error", err)
				}
				refreshCancel()
			}
		}
	}()

	health := agent.NewHealth()
	handler := http.NewServeMux()
	handler.Handle("/healthz", health.Handler())
	metricsHandler := metrics.NewHandler(health, []probe.MapStatsSource{execProbe, cpuProbe, runQLatProbe}, "/proc")
	metricsHandler.Timing = timing
	handler.Handle("/metrics", metricsHandler)
	handler.Handle("/api/v1/topology", topologyStore)
	handler.Handle("/api/v1/events", events)
	handler.Handle("/api/v1/cpu", cpuCollector)
	handler.Handle("/api/v1/cpu/accuracy", cpu.NewAccuracyHandler(cpu.TimedSource{Source: cpuProbe, Recorder: timing, Operation: telemetry.AccuracyScan}, topologyStore, *cgroupRoot))
	handler.Handle("/api/v1/runqlat", runQLatHandler)
	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErrors := make(chan error, 2)
	var debugServer *http.Server
	if *debugAddress != "" {
		debugMux := http.NewServeMux()
		debugMux.Handle("/diagnostics", telemetry.Handler(timing, "/proc", *cgroupRoot))
		debugMux.HandleFunc("/debug/pprof/", pprof.Index)
		debugMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		debugServer = &http.Server{Addr: *debugAddress, Handler: debugMux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			logger.Info("diagnostics listening (profiling changes collection cost)", "address", *debugAddress)
			if err := debugServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErrors <- err
			}
		}()
	}
	go func() {
		logger.Info("health API listening", "address", *listenAddress)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	probeErrors := make(chan error, 1)
	go func() {
		probeErrors <- execProbe.Run(ctx, func(kernelEvent probe.ExecEvent) {
			health.RecordEvent()
			record := events.Add(kernelEvent)
			if record.Workload == nil && kernelEvent.Type == probe.EventTypeExec {
				now := time.Now()
				unresolvedMu.Lock()
				if now.After(unresolvedUntil[kernelEvent.CgroupID]) {
					unresolvedUntil[kernelEvent.CgroupID] = now.Add(30 * time.Second)
					select {
					case refreshRequests <- struct{}{}:
					default:
					}
				}
				unresolvedMu.Unlock()
			}
			encoded, _ := json.Marshal(record)
			fmt.Println(string(encoded))
		})
	}()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if lost, err := execProbe.LostEvents(); err == nil {
					health.SetEventsLost(lost)
				}
			}
		}
	}()

	logger.Info("kno-agent started", "probes", "sched_process_exec,sched_process_exit,sched_switch,sched_wakeup,sched_wakeup_new", "runtimeEndpoint", *runtimeEndpoint, "node", *nodeName)
	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
	case err := <-probeErrors:
		if err != nil {
			logger.Error("eBPF probe stopped", "error", err)
		}
	case err := <-serverErrors:
		logger.Error("health API stopped", "error", err)
	}

	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		logger.Error("health API shutdown failed", "error", err)
	}
	if debugServer != nil {
		_ = debugServer.Shutdown(shutdownContext)
	}
}
