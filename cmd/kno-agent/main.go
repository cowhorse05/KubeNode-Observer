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
	"syscall"
	"time"

	"github.com/cilium/ebpf/btf"

	"github.com/cowhorse05/KubeNode-Observer/internal/agent"
	"github.com/cowhorse05/KubeNode-Observer/internal/cpu"
	eventstore "github.com/cowhorse05/KubeNode-Observer/internal/event"
	"github.com/cowhorse05/KubeNode-Observer/internal/host"
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
	kataMonitorEndpoint := flag.String("kata-monitor", "", "optional local Kata Monitor http://127.0.0.1:8090 endpoint")
	hostProc := flag.String("host-proc", "/host/proc", "host procfs mount")
	cgroupRoot := flag.String("cgroup-root", "/host/sys/fs/cgroup", "host cgroup v2 mount")
	topologyInterval := flag.Duration("topology-interval", 10*time.Second, "topology refresh interval")
	cpuInterval := flag.Duration("cpu-interval", time.Second, "CPU runtime sampling interval")
	mapStatsInterval := flag.Duration("map-stats-interval", 15*time.Second, "background map statistics refresh delay (1s..1h)")
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
	if *topologyInterval <= 0 || *cpuInterval <= 0 || *mapStatsInterval < time.Second || *mapStatsInterval > time.Hour {
		logger.Error("collection intervals must be positive; map stats interval must be 1s..1h", "topologyInterval", *topologyInterval, "cpuInterval", *cpuInterval, "mapStatsInterval", *mapStatsInterval)
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
	// All CO-RE relocations and attachments are complete. The library's global
	// kernel type cache is no longer needed by our static probes. Drop only the
	// Go type metadata, not program/map FDs; a future loader can reload the spec.
	// Do not force GC: normal runtime collection/scavenging reclaims the memory.
	btf.FlushKernelSpec()

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
	kataMonitor, err := topology.NewKataMonitor(*kataMonitorEndpoint, topologyStore)
	if err != nil {
		logger.Error("invalid Kata Monitor endpoint", "error", err)
		os.Exit(2)
	}
	topologyStore.Timing = timing
	go topologyStore.Run(ctx, *topologyInterval)
	events := eventstore.NewStore(4096, topologyStore)
	cpuCollector := cpu.NewCollector(cpu.TimedSource{Source: cpuProbe, Recorder: timing, Operation: telemetry.CPUScan}, topologyStore)
	cpuCollector.Timing = timing
	go cpuCollector.Run(ctx, *cpuInterval)
	runQLatHandler := runqlat.NewHandler(runQLatProbe, topologyStore)

	refreshQueue := topology.NewRefreshQueue(topologyStore)
	refreshContext, stopRefresh := context.WithCancel(ctx)
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		refreshQueue.Run(refreshContext)
	}()
	defer func() { stopRefresh(); <-refreshDone }()

	health := agent.NewHealth()
	mapCache, err := metrics.NewMapCache([]probe.MapStatsSource{execProbe, cpuProbe, runQLatProbe}, *mapStatsInterval)
	if err != nil {
		logger.Error("invalid map stats configuration", "error", err)
		return
	}
	mapCache.Timing = timing
	metricsCtx, stopMetrics := context.WithCancel(ctx)
	metricsDone := make(chan struct{})
	go func() { defer close(metricsDone); mapCache.Run(metricsCtx) }()
	// Join before deferred probe.Close calls: no refresh may use closed maps.
	defer func() { stopMetrics(); <-metricsDone }()
	handler := http.NewServeMux()
	handler.Handle("/healthz", health.Handler())
	metricsHandler := metrics.NewHandler(health, mapCache, "/proc")
	metricsHandler.Timing = timing
	handler.Handle("/metrics", metricsHandler)
	handler.Handle("/api/v1/topology", topologyStore)
	handler.Handle("/api/v1/kata/metrics", kataMonitor)
	handler.Handle("/api/v1/events", events)
	handler.Handle("/api/v1/cpu", cpuCollector)
	handler.Handle("/api/v1/cpu/accuracy", cpu.NewAccuracyHandler(cpu.TimedSource{Source: cpuProbe, Recorder: timing, Operation: telemetry.AccuracyScan}, topologyStore, *cgroupRoot))
	handler.Handle("/api/v1/runqlat", runQLatHandler)
	handler.Handle("/api/v1/host/resources", host.NewHandler(topologyStore, cpuProbe, runQLatProbe, host.NewProcReader(*hostProc, *cgroupRoot)))
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
				refreshQueue.Request(kernelEvent.CgroupID)
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
