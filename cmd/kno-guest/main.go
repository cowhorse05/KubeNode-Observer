package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cilium/ebpf/btf"
	"github.com/cilium/ebpf/rlimit"
	"github.com/cowhorse05/KubeNode-Observer/internal/guest"
	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
)

func main() {
	check := flag.Bool("check", false, "load Guest CPU/runqlat probes, report capabilities and exit")
	inspect := flag.Bool("inspect", false, "read this Probe's procfs/cgroup resource diagnostics without loading BPF")
	listen := flag.String("listen", ":18081", "Guest Pod-network listen address (trusted development only)")
	podUID := flag.String("pod-uid", os.Getenv("POD_UID"), "required Downward API Pod UID")
	flag.Parse()
	if !*check && !*inspect && *podUID == "" {
		fmt.Fprintln(os.Stderr, "POD_UID required (Downward API)")
		os.Exit(2)
	}
	result := map[string]interface{}{"domain": "guest", "ready": false}
	for name, path := range map[string]string{"kernelRelease": "/proc/sys/kernel/osrelease", "bootId": "/proc/sys/kernel/random/boot_id", "status": "/proc/self/status", "cgroup": "/proc/self/cgroup"} {
		data, err := os.ReadFile(path)
		if err == nil {
			result[name] = string(data)
		} else {
			result[name] = err.Error()
			if !*check && !*inspect && (name == "kernelRelease" || name == "bootId") {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	}
	if stat, err := os.Stat("/sys/kernel/btf/vmlinux"); err == nil {
		result["btfBytes"] = stat.Size()
	} else {
		result["btfError"] = err.Error()
	}
	if *inspect {
		for _, name := range []string{"memory.current", "memory.peak", "memory.max", "memory.events", "cpu.stat"} {
			data, err := os.ReadFile("/sys/fs/cgroup/" + name)
			if err == nil {
				result[name] = strings.TrimSpace(string(data))
			} else {
				result[name] = err.Error()
			}
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
		return
	}
	err := rlimit.RemoveMemlock()
	var cpu *probe.CPUProbe
	var latency *probe.RunQLatProbe
	if err == nil {
		cpu, err = probe.NewCPUProbe()
		if err == nil {
			defer cpu.Close()
			latency, err = probe.NewRunQLatProbe()
			if err == nil {
				defer latency.Close()
				_, err = cpu.Snapshot()
				if err == nil {
					_, err = latency.Snapshot()
				}
			}
		}
	}
	btf.FlushKernelSpec()
	if err != nil {
		result["error"] = err.Error()
	} else {
		result["ready"] = true
	}
	if *check {
		_ = json.NewEncoder(os.Stdout).Encode(result)
	}
	if err != nil {
		if !*check {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
	if *check {
		return
	}
	instance, err := guest.NewID()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	handler := &guest.Handler{Identity: guest.Response{PodUID: *podUID, KernelRelease: strings.TrimSpace(result["kernelRelease"].(string)), BootID: strings.TrimSpace(result["bootId"].(string)), InstanceID: instance}, CPU: cpu, Latency: latency, Proc: guest.ProcFS{Root: "/proc"}, Scheduler: latency}
	mux := http.NewServeMux()
	mux.Handle(guest.Path, handler)
	mux.Handle(guest.ClockPath, &guest.ClockHandler{PodUID: *podUID, InstanceID: instance, Source: guest.LinuxClock{}})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ready": true, "domain": "guest", "podUid": *podUID, "instanceId": instance})
	})
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	done := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				_ = server.Close()
			}
		case <-done:
		}
	}()
	fmt.Fprintln(os.Stdout, "Guest CPU/runqlat ready on", *listen)
	err = server.ListenAndServe()
	close(done)
	<-shutdownDone
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
