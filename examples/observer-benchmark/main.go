package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const maxLatencyUS = 10_000

type result struct {
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
	workers := flag.Int("workers", 8, "locked OS worker threads")
	workIterations := flag.Int("work-iterations", 64, "SHA-256 iterations per operation")
	flag.Parse()
	if *duration <= 0 || *workers <= 0 || *workIterations <= 0 {
		fmt.Fprintln(os.Stderr, "duration, workers and work-iterations must be positive")
		os.Exit(2)
	}

	start := make(chan struct{})
	deadline := time.Now().Add(*duration)
	histograms := make([][]uint64, *workers)
	for worker := range histograms {
		histograms[worker] = make([]uint64, maxLatencyUS+1)
	}
	counts := make([]uint64, *workers)
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
				for iteration := 0; iteration < *workIterations; iteration++ {
					payload = sha256.Sum256(payload[:])
				}
				_, _, _ = unix.Syscall(unix.SYS_SCHED_YIELD, 0, 0, 0)
				elapsedUS := uint64((time.Since(operationStart) + time.Microsecond - 1) / time.Microsecond)
				if elapsedUS > maxLatencyUS {
					elapsedUS = maxLatencyUS
				}
				histograms[worker][elapsedUS]++
				counts[worker]++
			}
			if payload[0] == 255 {
				fmt.Fprint(os.Stderr, "")
			}
		}()
	}
	startedAt := time.Now()
	deadline = startedAt.Add(*duration)
	close(start)
	wait.Wait()
	elapsed := time.Since(startedAt)

	histogram := make([]uint64, maxLatencyUS+1)
	var operations uint64
	for worker := range histograms {
		operations += counts[worker]
		for bucket, count := range histograms[worker] {
			histogram[bucket] += count
		}
	}
	response := result{
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
