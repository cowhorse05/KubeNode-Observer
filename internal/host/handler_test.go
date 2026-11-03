package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type fakeTopology struct {
	snapshot *topology.Snapshot
	err      error
	end      *topology.Snapshot
	calls    int
}

func (f *fakeTopology) CurrentSnapshot() (*topology.Snapshot, error) {
	f.calls++
	if f.calls > 1 && f.end != nil {
		return f.end, f.err
	}
	return f.snapshot, f.err
}

type fakeCPU struct {
	first, last probe.CPUSnapshot
	calls       int
	err         error
}

func (f *fakeCPU) Snapshot() (probe.CPUSnapshot, error) {
	f.calls++
	if f.calls == 1 {
		return f.first, f.err
	}
	return f.last, f.err
}

type fakeLatency struct {
	first, last []probe.RunQLatHistogram
	calls       int
}

func (f *fakeLatency) Snapshot() ([]probe.RunQLatHistogram, error) {
	f.calls++
	if f.calls == 1 {
		return f.first, nil
	}
	return f.last, nil
}

type fakeProcesses struct {
	first, last ProcessView
	calls       int
	err         error
}

func (f *fakeProcesses) ReadVMM(topology.HostProcess) (ProcessView, error) {
	f.calls++
	if f.calls == 1 {
		return f.first, f.err
	}
	return f.last, f.err
}
func (f *fakeProcesses) ContainerIdentity(c topology.Container) (topology.HostProcess, error) {
	return topology.HostProcess{PID: c.PID, CgroupID: c.CgroupID}, f.err
}
func (f *fakeProcesses) Validate(topology.HostProcess) error { return f.err }
func (f *fakeProcesses) ContainerMemory(string) (*uint64, error) {
	value := uint64(2048)
	return &value, f.err
}

func fakeHandler() (*Handler, *fakeTopology) {
	b, a, c0, c1, l0, l1 := fixture()
	snapshot := &topology.Snapshot{Sandboxes: []topology.Sandbox{{ID: strings.Repeat("a", 64), Namespace: "ns", PodName: "pod", PodUID: "uid", Hypervisor: &b.Identity}}}
	topologySource := &fakeTopology{snapshot: snapshot}
	return NewHandler(topologySource, &fakeCPU{first: c0, last: c1}, &fakeLatency{first: l0, last: l1}, &fakeProcesses{first: b, last: a}), topologySource
}

func TestHostHTTPValidationAndBusy(t *testing.T) {
	h, _ := fakeHandler()
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/", 405}, {"GET", "/?duration=0s", 400}, {"GET", "/?duration=31s", 400}, {"GET", "/?limit=101", 400},
		{"GET", "/?kind=guest", 400}, {"GET", "/?sandbox=../x", 400}, {"GET", "/?duration=1s&duration=2s", 400}, {"GET", "/?url=http://example.com", 400},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status || !strings.Contains(w.Header().Get("Content-Type"), "json") {
			t.Fatal(tc, w.Code, w.Body.String())
		}
		var body map[string]string
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["code"] == "" {
			t.Fatal("unstructured error")
		}
	}
	h.mu.Lock()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	h.mu.Unlock()
	if w.Code != 429 {
		t.Fatal(w.Code)
	}
}

func TestHostHTTPSuccessAndDeletedTarget(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		h, topo := fakeHandler()
		if deleted {
			topo.end = &topology.Snapshot{}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/?duration=100ms&namespace=ns&pod=pod", nil))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		var got Response
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Domain != "host" || got.TotalItems != 1 || len(got.Items) != 1 {
			t.Fatal(got)
		}
		if deleted {
			if got.Items[0].CPU != nil || !got.Items[0].Partial {
				t.Fatal("deleted VM got live counters")
			}
		} else if got.Items[0].CPU == nil || got.Items[0].CPU.Percent != 61 {
			t.Fatal("valid window missing")
		}
	}
}

func TestHostSourceErrorsEmptySelectionAndCancellation(t *testing.T) {
	h, topo := fakeHandler()
	topo.err = fmt.Errorf("stale")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	h, _ = fakeHandler()
	h.cpu = &fakeCPU{err: fmt.Errorf("probe error")}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	h, topo = fakeHandler()
	topo.snapshot = &topology.Snapshot{}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || h.cpu.(*fakeCPU).calls != 0 {
		t.Fatal("empty view scanned probes")
	}
	h, _ = fakeHandler()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.measure(ctx, selection{duration: time.Second, limit: 20})
	if err != context.Canceled {
		t.Fatal(err)
	}
}

func TestUnifiedTargetsDeduplicateVMAndKeepContainerIdentities(t *testing.T) {
	s := &topology.Snapshot{Sandboxes: []topology.Sandbox{{ID: "vm", PodUID: "uid"}}, Containers: []topology.Container{
		{ID: "guest1", SandboxID: "vm", ProcessVisibility: "guest-unavailable"}, {ID: "guest2", SandboxID: "vm", ProcessVisibility: "guest-unavailable"},
		{ID: "c1", SandboxID: "runc", PodUID: "uid2", ProcessVisibility: "host", PID: 1, CgroupID: 1}, {ID: "c2", SandboxID: "runc", PodUID: "uid2", ProcessVisibility: "host", PID: 2, CgroupID: 2},
	}}
	rows, err := targets(s, selection{kind: "all"})
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	if !sameTarget(rows[2], rows) {
		t.Fatal("second runc container falsely marked replaced")
	}
	for len(s.Sandboxes) < 257 {
		s.Sandboxes = append(s.Sandboxes, topology.Sandbox{ID: fmt.Sprint(len(s.Sandboxes))})
	}
	if _, err := targets(s, selection{kind: "all"}); err == nil {
		t.Fatal("row bound ignored")
	}
}
