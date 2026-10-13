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
)

func main() {
	listenAddress := flag.String("listen", ":18080", "health API listen address")
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

	health := agent.NewHealth()
	server := &http.Server{
		Addr:              *listenAddress,
		Handler:           health.Handler(),
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

	logger.Info("kno-agent started", "probe", "sched_process_exec")
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
