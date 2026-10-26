package metrics

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
)

type mapSample struct {
	stats    []probe.BPFMapStats
	success  time.Time
	duration time.Duration
	failed   bool
	errors   uint64
}

type mapSnapshot struct{ sources []mapSample }

// MapCache has one bounded background refresher, never one per HTTP request.
// Published snapshots and their stats slices are immutable.
type MapCache struct {
	Timing    *telemetry.Recorder // Set before Run.
	sources   []probe.MapStatsSource
	interval  time.Duration
	ttl       time.Duration
	refreshMu sync.Mutex
	snapshot  atomic.Pointer[mapSnapshot]
}

func NewMapCache(sources []probe.MapStatsSource, interval time.Duration) (*MapCache, error) {
	if interval < time.Second || interval > time.Hour {
		return nil, fmt.Errorf("map stats interval must be 1s..1h")
	}
	for _, source := range sources {
		if source == nil {
			return nil, fmt.Errorf("nil map stats source")
		}
	}
	c := &MapCache{sources: slices.Clone(sources), interval: interval, ttl: 3 * interval}
	c.snapshot.Store(&mapSnapshot{sources: make([]mapSample, len(sources))})
	return c, nil
}

func (c *MapCache) Run(ctx context.Context) {
	for {
		c.Refresh(ctx)
		// Schedule from completion: slow refreshes must not trigger catch-up scans.
		timer := time.NewTimer(c.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *MapCache) Refresh(ctx context.Context) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	for index, source := range c.sources {
		if ctx.Err() != nil {
			return
		}
		started := time.Now()
		stats, err := source.MapStats()
		elapsed := time.Since(started)
		if index < 3 {
			c.Timing.Observe(telemetry.MetricsExec+telemetry.Operation(index), started)
		}
		previous := c.snapshot.Load()
		next := &mapSnapshot{sources: slices.Clone(previous.sources)}
		current := &next.sources[index]
		current.duration, current.failed = elapsed, err != nil
		if err != nil {
			current.errors++
		} else {
			current.stats = slices.Clone(stats)
			current.success = started
		}
		// One slow source does not hide already completed sources. Never mutate a
		// slice after publishing it, including on the following refresh iteration.
		c.snapshot.Store(next)
	}
}

func sourceName(index int) string {
	if index < 3 {
		return [...]string{"exec", "cpu", "runqlat"}[index]
	}
	return fmt.Sprintf("source_%d", index)
}
