package main

import (
	"flag"
	"fmt"
	"runtime"
	"sync"
	"time"
)

func main() {
	workers := flag.Int("workers", 1, "number of CPU-saturating workers")
	duration := flag.Duration("duration", 2*time.Minute, "time to consume CPU")
	flag.Parse()
	if *workers < 1 {
		panic("workers must be positive")
	}
	runtime.GOMAXPROCS(*workers)

	stop := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(*workers)
	for worker := 0; worker < *workers; worker++ {
		go func(seed uint64) {
			defer wait.Done()
			value := seed + 1
			for {
				for iteration := 0; iteration < 1_000_000; iteration++ {
					value = value*1664525 + 1013904223
				}
				select {
				case <-stop:
					runtime.KeepAlive(value)
					return
				default:
				}
			}
		}(uint64(worker))
	}

	fmt.Printf("running %d CPU worker(s) for %s\n", *workers, *duration)
	time.Sleep(*duration)
	close(stop)
	wait.Wait()
}
