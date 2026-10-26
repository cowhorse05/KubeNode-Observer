package metrics

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
)

type mapSourceFunc func() ([]probe.BPFMapStats, error)

func (f mapSourceFunc) MapStats() ([]probe.BPFMapStats, error) { return f() }

func renderCache(cache *MapCache) string {
	w := httptest.NewRecorder()
	NewHandler(fakeHealth{}, cache, "/nonexistent-proc-fixture").ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	return w.Body.String()
}

func TestCacheLifecycleAndStaleness(t *testing.T) {
	fail := false
	stats := []probe.BPFMapStats{{Name: "cpu_runtime", Type: "LRUHash", Entries: 7, MaxEntries: 8192}}
	c, err := NewMapCache([]probe.MapStatsSource{mapSourceFunc(func() ([]probe.BPFMapStats, error) {
		if fail {
			return nil, errors.New("read failed")
		}
		return stats, nil
	})}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	initial := renderCache(c)
	if !strings.Contains(initial, `kno_agent_bpf_map_stats_ready{source="exec"} 0`) || strings.Contains(initial, `kno_agent_bpf_map_entries{map=`) {
		t.Fatal(initial)
	}
	c.Refresh(context.Background())
	stats[0].Entries = 900 // Source-owned slice must not alias the published cache.
	good := c.snapshot.Load().sources[0]
	if good.stats[0].Entries != 7 {
		t.Fatal("source slice aliases snapshot")
	}
	fail = true
	c.Refresh(context.Background())
	failed := c.snapshot.Load().sources[0]
	if failed.success != good.success || !failed.failed || failed.errors != 1 || failed.stats[0].Entries != 7 {
		t.Fatalf("failed refresh: %+v", failed)
	}
	body := renderCache(c)
	if !strings.Contains(body, `kno_agent_bpf_map_stats_ready{source="exec"} 1`) || !strings.Contains(body, `kno_agent_bpf_map_stats_refresh_error{source="exec"} 1`) || !strings.Contains(body, `kno_agent_bpf_map_entries{map="cpu_runtime",type="LRUHash"} 7`) {
		t.Fatal(body)
	}
	// Expire without sleeping; store a new immutable snapshot.
	failed.success = time.Now().Add(-4 * time.Second)
	c.snapshot.Store(&mapSnapshot{sources: []mapSample{failed}})
	body = renderCache(c)
	if strings.Contains(body, `kno_agent_bpf_map_entries{map=`) || !strings.Contains(body, `kno_agent_bpf_map_stats_ready{source="exec"} 0`) {
		t.Fatal(body)
	}
	fail = false
	c.Refresh(context.Background())
	recovered := c.snapshot.Load().sources[0]
	if recovered.failed || recovered.errors != 1 || recovered.stats[0].Entries != 900 {
		t.Fatalf("recovery: %+v", recovered)
	}
	if _, err := NewMapCache(nil, 0); err == nil {
		t.Fatal("invalid interval accepted")
	}
	if _, err := NewMapCache([]probe.MapStatsSource{nil}, time.Second); err == nil {
		t.Fatal("nil source accepted")
	}
}

func TestScrapesNeverRefreshOrWaitForMaps(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	c, _ := NewMapCache([]probe.MapStatsSource{mapSourceFunc(func() ([]probe.BPFMapStats, error) {
		calls.Add(1)
		close(started)
		<-release
		return fakeMaps{}.MapStats()
	})}, time.Second)
	done := make(chan struct{})
	go func() { defer close(done); c.Refresh(context.Background()) }()
	<-started
	var readers sync.WaitGroup
	for i := 0; i < 20; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 10; j++ {
				_ = renderCache(c)
			}
		}()
	}
	readDone := make(chan struct{})
	go func() { readers.Wait(); close(readDone) }()
	select {
	case <-readDone:
	case <-time.After(5 * time.Second):
		close(release)
		<-done
		t.Fatal("scrape blocked on map refresh")
	}
	if calls.Load() != 1 {
		t.Fatal("HTTP requests triggered map scans")
	}
	close(release)
	<-done
	for i := 0; i < 10; i++ {
		_ = renderCache(c)
	}
	if calls.Load() != 1 {
		t.Fatal("cache hit triggered refresh")
	}
}

func TestRunCancellation(t *testing.T) {
	called := make(chan struct{}, 1)
	c, _ := NewMapCache([]probe.MapStatsSource{mapSourceFunc(func() ([]probe.BPFMapStats, error) { called <- struct{}{}; return nil, nil })}, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	<-called
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cache did not stop")
	}
	before := c.snapshot.Load().sources[0].success
	c.Refresh(ctx)
	if c.snapshot.Load().sources[0].success != before {
		t.Fatal("canceled refresh called source")
	}
}

func TestSourcesPublishIndependently(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	c, _ := NewMapCache([]probe.MapStatsSource{fakeMaps{}, mapSourceFunc(func() ([]probe.BPFMapStats, error) { close(started); <-release; return nil, errors.New("slow failure") })}, time.Second)
	done := make(chan struct{})
	go func() { defer close(done); c.Refresh(context.Background()) }()
	<-started
	first := c.snapshot.Load()
	if first.sources[0].success.IsZero() || !first.sources[1].success.IsZero() {
		close(release)
		<-done
		t.Fatal("fast source hidden by slow source")
	}
	close(release)
	<-done
	if first.sources[1].failed || !c.snapshot.Load().sources[1].failed {
		t.Fatal("published snapshot was mutated")
	}
}
