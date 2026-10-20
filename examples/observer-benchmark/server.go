package main

import (
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

func serve(address string, iterations int) {
	var mu sync.Mutex
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if mu.TryLock() {
			mu.Unlock()
			fmt.Fprintln(w, `{"running":false}`)
		} else {
			fmt.Fprintln(w, `{"running":true}`)
		}
	})
	mux.HandleFunc("GET /run", func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("mode")
		duration, e1 := time.ParseDuration(r.URL.Query().Get("duration"))
		warmup, e2 := time.ParseDuration(r.URL.Query().Get("warmup"))
		workers, e3 := strconv.Atoi(r.URL.Query().Get("workers"))
		if e1 != nil || e2 != nil || e3 != nil || duration < time.Second || duration > 2*time.Minute || warmup < 0 || warmup > 30*time.Second || workers < 1 || workers > 64 || (mode != "steady" && mode != "churn") {
			http.Error(w, "invalid workload parameters", 400)
			return
		}
		if !mu.TryLock() {
			http.Error(w, "workload already running", 429)
			return
		}
		defer mu.Unlock()
		command := exec.CommandContext(r.Context(), "/benchmark", "--duration="+duration.String(), "--warmup="+warmup.String(), "--workers="+strconv.Itoa(workers), "--mode="+mode, "--work-iterations="+strconv.Itoa(iterations))
		output, err := command.Output()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(output)
	})
	server := &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		panic(err)
	}
}
