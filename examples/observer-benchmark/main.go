package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const maxLatencyUS = 10_000

type result struct {
	Mode            string  `json:"mode"`
	WarmupSeconds   float64 `json:"warmupSeconds"`
	LatencyOverflow uint64  `json:"latencyOverflow"`
	DurationSeconds float64 `json:"durationSeconds"`
	Workers         int     `json:"workers"`
	WorkIterations  int     `json:"workIterations"`
	Operations      uint64  `json:"operations"`
	Throughput      float64 `json:"throughputOpsPerSecond"`
	P50US           uint64  `json:"p50Us"`
	P95US           uint64  `json:"p95Us"`
	P99US           uint64  `json:"p99Us"`
}

func main() {
	duration := flag.Duration("duration", 8*time.Second, "measurement duration")
	warmup := flag.Duration("warmup", 3*time.Second, "unrecorded warmup")
	mode := flag.String("mode", "steady", "steady or churn workload")
	listen := flag.String("listen", "", "serve controlled benchmark requests")
	child := flag.Bool("child", false, "execute one short-lived operation")
	workers := flag.Int("workers", 8, "locked OS worker threads")
	workIterations := flag.Int("work-iterations", 64, "SHA-256 iterations per operation")
	flag.Parse()
	if *duration <= 0 || *workers <= 0 || *workers > 64 || *workIterations <= 0 || *warmup < 0 || (*mode != "steady" && *mode != "churn") {
		fmt.Fprintln(os.Stderr, "duration, workers and work-iterations must be positive")
		os.Exit(2)
	}
	if *child {
		payload := sha256.Sum256([]byte("kno-child"))
		for i := 0; i < *workIterations; i++ {
			payload = sha256.Sum256(payload[:])
		}
		runtime.KeepAlive(payload)
		return
	}
	if *listen != "" {
		serve(*listen, *workIterations)
		return
	}

	start := make(chan struct{})
	deadline := time.Now().Add(*duration)
	histograms := make([][]uint64, *workers)
	for worker := range histograms {
		histograms[worker] = make([]uint64, maxLatencyUS+1)
	}
	counts := make([]uint64, *workers)
	overflows := make([]uint64, *workers)
	failures := make(chan error, *workers)
	var measurementStart time.Time
	var wait sync.WaitGroup
	wait.Add(*workers)
	for worker := 0; worker < *workers; worker++ {
		worker := worker
		go func() {
			defer wait.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			<-start
			payload := sha256.Sum256([]byte(fmt.Sprintf("kno-worker-%d", worker)))
			for time.Now().Before(deadline) {
				operationStart := time.Now()
				if *mode == "churn" {
					command := exec.Command("/benchmark", "--child", fmt.Sprintf("--work-iterations=%d", *workIterations))
					if err := command.Run(); err != nil {
						failures <- err
						return
					}
				} else {
					for iteration := 0; iteration < *workIterations; iteration++ {
						payload = sha256.Sum256(payload[:])
					}
					_, _, _ = unix.Syscall(unix.SYS_SCHED_YIELD, 0, 0, 0)
				}
				if operationStart.Before(measurementStart) {
					continue
				}
				elapsedUS := uint64((time.Since(operationStart) + time.Microsecond - 1) / time.Microsecond)
				if elapsedUS > maxLatencyUS {
					elapsedUS = maxLatencyUS
					overflows[worker]++
				}
				histograms[worker][elapsedUS]++
				counts[worker]++
			}
			runtime.KeepAlive(payload)
		}()
	}
	startedAt := time.Now()
	measurementStart = startedAt.Add(*warmup)
	deadline = measurementStart.Add(*duration)
	close(start)
	wait.Wait()
	select {
	case err := <-failures:
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	default:
	}
	elapsed := time.Since(measurementStart)

	histogram := make([]uint64, maxLatencyUS+1)
	var operations, overflow uint64
	for worker := range histograms {
		operations += counts[worker]
		overflow += overflows[worker]
		for bucket, count := range histograms[worker] {
			histogram[bucket] += count
		}
	}
	response := result{
		Mode: *mode, WarmupSeconds: warmup.Seconds(), LatencyOverflow: overflow,
		DurationSeconds: elapsed.Seconds(),
		Workers:         *workers,
		WorkIterations:  *workIterations,
		Operations:      operations,
		Throughput:      float64(operations) / elapsed.Seconds(),
		P50US:           percentileUS(histogram, operations, 50),
		P95US:           percentileUS(histogram, operations, 95),
		P99US:           percentileUS(histogram, operations, 99),
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func percentileUS(histogram []uint64, total, percentile uint64) uint64 {
	if total == 0 {
		return 0
	}
	target := (total*percentile + 99) / 100
	var cumulative uint64
	for bucket, count := range histogram {
		cumulative += count
		if cumulative >= target {
			return uint64(bucket)
		}
	}
	return 0
}
