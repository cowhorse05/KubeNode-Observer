package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	workers := flag.Int("workers", 1, "number of workers pinned to one CPU")
	duration := flag.Duration("duration", 2*time.Minute, "time to run")
	flag.Parse()
	if *workers < 1 {
		fmt.Fprintln(os.Stderr, "workers must be positive")
		os.Exit(2)
	}

	cpu, err := firstAllowedCPU()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	runtime.GOMAXPROCS(*workers)
	stop := make(chan struct{})
	ready := make(chan error, *workers)
	var wait sync.WaitGroup
	wait.Add(*workers)
	for worker := 0; worker < *workers; worker++ {
		go func(seed uint64) {
			defer wait.Done()
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			var affinity unix.CPUSet
			affinity.Zero()
			affinity.Set(cpu)
			if err := unix.SchedSetaffinity(0, &affinity); err != nil {
				ready <- err
				return
			}
			ready <- nil

			value := seed + 1
			for {
				for iteration := 0; iteration < 100_000; iteration++ {
					value = value*1664525 + 1013904223
				}
				_, _, _ = unix.Syscall(unix.SYS_SCHED_YIELD, 0, 0, 0)
				select {
				case <-stop:
					runtime.KeepAlive(value)
					return
				default:
				}
			}
		}(uint64(worker))
	}
	for worker := 0; worker < *workers; worker++ {
		if err := <-ready; err != nil {
			fmt.Fprintf(os.Stderr, "pin worker to CPU %d: %v\n", cpu, err)
			close(stop)
			wait.Wait()
			os.Exit(1)
		}
	}

	fmt.Printf("running %d worker(s) on CPU %d for %s\n", *workers, cpu, *duration)
	time.Sleep(*duration)
	close(stop)
	wait.Wait()
}

func firstAllowedCPU() (int, error) {
	var allowed unix.CPUSet
	if err := unix.SchedGetaffinity(0, &allowed); err != nil {
		return 0, fmt.Errorf("read CPU affinity: %w", err)
	}
	for cpu := 0; cpu < 1024; cpu++ {
		if allowed.IsSet(cpu) {
			return cpu, nil
		}
	}
	return 0, fmt.Errorf("CPU affinity set is empty")
}
