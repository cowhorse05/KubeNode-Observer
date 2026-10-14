package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/agent"
	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

func main() {
	listenAddress := flag.String("listen", ":18080", "health API listen address")
	runtimeEndpoint := flag.String("runtime-endpoint", "unix:///run/containerd/containerd.sock", "CRI runtime endpoint")
	hostProc := flag.String("host-proc", "/host/proc", "host procfs mount")
	cgroupRoot := flag.String("cgroup-root", "/host/sys/fs/cgroup", "host cgroup v2 mount")
	topologyInterval := flag.Duration("topology-interval", 10*time.Second, "topology refresh interval")
	nodeName := flag.String("node-name", os.Getenv("NODE_NAME"), "Kubernetes node name")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	execProbe, err := probe.NewExecProbe()
	if err != nil {
		logger.Error("failed to initialize eBPF probe", "error", err)
		os.Exit(1)
	}
	defer execProbe.Close()

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
	topologyStore := topology.NewStore(topologyCollector)
	go topologyStore.Run(ctx, *topologyInterval)

	health := agent.NewHealth()
	handler := http.NewServeMux()
	handler.Handle("/healthz", health.Handler())
	handler.Handle("/api/v1/topology", topologyStore)
	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("health API listening", "address", *listenAddress)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	probeErrors := make(chan error, 1)
	go func() {
		probeErrors <- execProbe.Run(ctx, func(event probe.ExecEvent) {
			health.RecordEvent()
			payload := map[string]any{
				"type":        "exec",
				"timestampNs": event.TimestampNS,
				"cgroupId":    event.CgroupID,
				"pid":         event.PID,
				"tgid":        event.TGID,
				"comm":        event.Command(),
			}
			encoded, _ := json.Marshal(payload)
			fmt.Println(string(encoded))
		})
	}()

	logger.Info("kno-agent started", "probe", "sched_process_exec", "runtimeEndpoint", *runtimeEndpoint, "node", *nodeName)
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
}
