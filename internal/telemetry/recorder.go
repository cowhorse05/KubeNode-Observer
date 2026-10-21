// Package telemetry measures low-frequency user-space collection stages.
// Durations are wall time (including preemption/throttling), not CPU attribution.
package telemetry

import (
	"sync"
	"time"
)

type Operation int

const (
	CPUScan Operation = iota
	AccuracyScan
	CPUAggregate
	TopologyCRI
	TopologyEnrich
	TopologyRefresh
	MetricsExec
	MetricsCPU
	MetricsRunQLat
	MetricsScrape
	operationCount
)

var names = [...]string{"cpu.scan", "accuracy.scan", "cpu.aggregate", "topology.cri", "topology.enrich", "topology.refresh", "metrics.exec_maps", "metrics.cpu_maps", "metrics.runqlat_maps", "metrics.scrape"}

type Stage struct {
	Count     uint64 `json:"count"`
	TotalNS   uint64 `json:"totalNs"`
	MaxNS     uint64 `json:"maxNs"`
	LastNS    uint64 `json:"lastNs"`
	Over100MS uint64 `json:"over100ms"`
	Over250MS uint64 `json:"over250ms"`
	Over1S    uint64 `json:"over1s"`
}

type Recorder struct {
	mu     sync.Mutex
	stages [operationCount]Stage
}

// Observe is a no-op when instrumentation is disabled (nil recorder).
func (r *Recorder) Observe(op Operation, started time.Time) {
	if r != nil {
		r.record(op, time.Since(started))
	}
}

func (r *Recorder) record(op Operation, elapsed time.Duration) {
	if op < 0 || op >= operationCount || elapsed < 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &r.stages[op]
	s.Count++
	s.LastNS = uint64(elapsed)
	s.TotalNS += s.LastNS
	if s.LastNS > s.MaxNS {
		s.MaxNS = s.LastNS
	}
	if elapsed > 100*time.Millisecond {
		s.Over100MS++
	}
	if elapsed > 250*time.Millisecond {
		s.Over250MS++
	}
	if elapsed > time.Second {
		s.Over1S++
	}
}

func (r *Recorder) Snapshot() map[string]Stage {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[string]Stage, operationCount)
	for op, stage := range r.stages {
		result[names[op]] = stage
	}
	return result
}
