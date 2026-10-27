package cpu

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
)

type batchSource struct {
	batches []probe.CPUSnapshot
	index   int
}

func (s *batchSource) Snapshot() (probe.CPUSnapshot, error) {
	b := s.batches[s.index]
	s.index++
	return b, nil
}

func TestContainerTotalsSurviveMissingProcessDetail(t *testing.T) {
	start := time.Unix(100, 0)
	source := &batchSource{batches: []probe.CPUSnapshot{
		{CapturedAt: start, Processes: []probe.CPURuntime{runtime(42, 10, 10, 100, "short")}, Totals: []probe.CgroupRuntime{{CgroupID: 42, Runtime: 100}}},
		{CapturedAt: start.Add(time.Second), Totals: []probe.CgroupRuntime{{CgroupID: 42, Runtime: 500000100}}},
	}}
	c := NewCollector(source, fakeResolver{})
	// Passed wall time is deliberately wrong; use the actual scan timestamp.
	if err := c.Collect(start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{"pod", "container", "process"} {
		w := httptest.NewRecorder()
		c.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/cpu?level="+level+"&namespace=default&pod=demo", nil))
		var response Response
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.WindowSeconds != 1 {
			t.Fatal(response)
		}
		if level == "process" {
			if len(response.Items) != 0 || response.Accounting != "process" {
				t.Fatal(response)
			}
		} else if len(response.Items) != 1 || response.Items[0].RuntimeNS != 500000000 || response.Items[0].CPUPercent != 50 || response.Accounting != "cgroup" {
			t.Fatal(response)
		}
	}
}

func TestEmptyTotalsDoNotFallBackToProcessSum(t *testing.T) {
	source := &batchSource{batches: []probe.CPUSnapshot{
		{Totals: []probe.CgroupRuntime{}},
		{Processes: []probe.CPURuntime{runtime(42, 10, 10, 100, "detail")}, Totals: []probe.CgroupRuntime{}},
	}}
	c := NewCollector(source, fakeResolver{})
	start := time.Unix(100, 0)
	_ = c.Collect(start)
	_ = c.Collect(start.Add(time.Second))
	w := httptest.NewRecorder()
	c.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/cpu?level=pod", nil))
	var response Response
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 0 || response.Accounting != "cgroup" {
		t.Fatal(response)
	}
}

func TestCgroupAccuracyDeltaRejectsReset(t *testing.T) {
	old, current := uint64(100), uint64(350)
	sample := AccuracySample{CgroupRuntimeNS: &current}
	if err := applyCgroupDelta(&sample, AccuracySample{CgroupRuntimeNS: &old}); err != nil || sample.ObservedRuntimeNS != 250 {
		t.Fatalf("%+v %v", sample, err)
	}
	current = 90
	if err := applyCgroupDelta(&sample, AccuracySample{CgroupRuntimeNS: &old}); err == nil {
		t.Fatal("reset accepted")
	}
	if err := applyCgroupDelta(&sample, AccuracySample{}); err == nil {
		t.Fatal("missing baseline accepted")
	}
}
