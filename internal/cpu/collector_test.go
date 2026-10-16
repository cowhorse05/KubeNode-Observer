package cpu

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type fakeSource struct {
	snapshots [][]probe.CPURuntime
	index     int
}

func (f *fakeSource) Snapshot() ([]probe.CPURuntime, error) {
	result := f.snapshots[f.index]
	if f.index < len(f.snapshots)-1 {
		f.index++
	}
	return result, nil
}

type fakeResolver struct{}

func (fakeResolver) Resolve(cgroupID uint64) (topology.Container, string, bool) {
	if cgroupID != 42 {
		return topology.Container{}, "unresolved", false
	}
	return topology.Container{
		ID: "container-a", Name: "app", Namespace: "default", PodName: "demo", PodUID: "pod-a", CgroupID: 42,
	}, "current", true
}

func runtime(cgroup uint64, tgid, pid uint32, total uint64, command string) probe.CPURuntime {
	result := probe.CPURuntime{CgroupID: cgroup, TGID: tgid, PID: pid, Runtime: total}
	copy(result.Comm[:], command)
	return result
}

func TestCollectorAggregatesThreadsAndCalculatesPercent(t *testing.T) {
	source := &fakeSource{snapshots: [][]probe.CPURuntime{
		{runtime(42, 10, 10, 100, "worker"), runtime(42, 10, 11, 200, "thread")},
		{runtime(42, 10, 10, 300000100, "worker"), runtime(42, 10, 11, 200000200, "thread"), runtime(99, 20, 20, 1000, "host")},
	}}
	collector := NewCollector(source, fakeResolver{})
	start := time.Unix(100, 0)
	if err := collector.Collect(start); err != nil {
		t.Fatal(err)
	}
	if err := collector.Collect(start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	snapshot := collector.snapshot.Load()
	if len(snapshot.Processes) != 1 {
		t.Fatalf("processes = %#v", snapshot.Processes)
	}
	usage := snapshot.Processes[0]
	if usage.RuntimeNS != 500000000 || usage.CPUPercent != 50 || usage.PID != 10 || usage.Command != "worker" {
		t.Fatalf("usage = %#v", usage)
	}
	if snapshot.UnresolvedTasks != 1 {
		t.Fatalf("unresolved tasks = %d", snapshot.UnresolvedTasks)
	}
}

func TestCollectorHTTPAggregatesPod(t *testing.T) {
	collector := NewCollector(&fakeSource{snapshots: [][]probe.CPURuntime{
		{runtime(42, 10, 10, 0, "worker")},
		{runtime(42, 10, 10, 250000000, "worker")},
	}}, fakeResolver{})
	start := time.Unix(100, 0)
	_ = collector.Collect(start)
	_ = collector.Collect(start.Add(time.Second))

	request := httptest.NewRequest(http.MethodGet, "/api/v1/cpu?level=pod&namespace=default&limit=5", nil)
	response := httptest.NewRecorder()
	collector.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload Response
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 || payload.Items[0].Scope != "pod" || payload.Items[0].CPUPercent != 25 {
		t.Fatalf("response = %#v", payload)
	}
}
