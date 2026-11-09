package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
)

func fixture() (ProcView, ProcView, probe.CPUSnapshot, probe.CPUSnapshot, []probe.RunQLatHistogram, []probe.RunQLatHistogram) {
	b := ProcView{PID: 100, Start: 50, Command: "guest-burn", Threads: map[uint32]Task{100: {100, 50, "guest-burn"}, 101: {101, 51, "guest-burn"}}}
	a := b
	a.Threads = map[uint32]Task{100: b.Threads[100], 101: b.Threads[101]}
	at := time.Unix(10, 0)
	c0 := probe.CPUSnapshot{DetailsCapturedAt: at, Processes: []probe.CPURuntime{{CgroupID: 9, TGID: 700, PID: 700, NamespaceTGID: 100, NamespacePID: 100, Runtime: 100}, {CgroupID: 9, TGID: 700, PID: 701, NamespaceTGID: 100, NamespacePID: 101, Runtime: 100}}}
	c1 := probe.CPUSnapshot{DetailsCapturedAt: at.Add(time.Second), Processes: append([]probe.CPURuntime(nil), c0.Processes...)}
	c1.Processes[0].Runtime += 1000000
	c1.Processes[1].Runtime += 499000000
	c1.Processes = append(c1.Processes, probe.CPURuntime{CgroupID: 9, TGID: 100, PID: 101, Runtime: 9000000000}, probe.CPURuntime{CgroupID: 9, TGID: 900, PID: 901, NamespaceTGID: 200, NamespacePID: 101, Runtime: 9000000000})
	l0 := []probe.RunQLatHistogram{{CgroupID: 9, TGID: 700, PID: 700, TotalNS: 100, Count: 1}, {CgroupID: 9, TGID: 700, PID: 701, TotalNS: 100, Count: 1}}
	l0[0].Buckets[5] = 1
	l0[1].Buckets[5] = 1
	l1 := append([]probe.RunQLatHistogram(nil), l0...)
	l1[0].Count += 3
	l1[0].TotalNS += 96000
	l1[0].Buckets[5] += 3
	l1[1].Count += 4
	l1[1].TotalNS += 128000
	l1[1].Buckets[5] += 4
	return b, a, c0, c1, l0, l1
}
func TestGuestProcessUsesNamespaceAliasesNotHostNumericalPID(t *testing.T) {
	b, a, c0, c1, l0, l1 := fixture()
	row := measureProcess(Process{}, b, a, c0, c1, l0, l1)
	if row.CPU == nil || row.CPU.Percent != 50 || row.PID != 700 || row.CgroupID != 9 || row.RunQLat == nil || row.RunQLat.Count != 7 || row.RunQLat.P95US != 63 || len(row.Issues) > 0 {
		t.Fatalf("wrong Guest join: %+v", row)
	}
}
func TestGuestProcessRejectsIncompleteIdentityAndCounters(t *testing.T) {
	for _, scenario := range []string{"missing", "collision", "counter-reset", "global-key", "cgroup-migration", "new-thread", "gone-thread", "thread-reuse", "renamed", "bad-window", "mixed-cgroup"} {
		t.Run(scenario, func(t *testing.T) {
			b, a, c0, c1, l0, l1 := fixture()
			switch scenario {
			case "missing":
				c1.Processes[1].NamespacePID = 0
			case "collision":
				c1.Processes = append(c1.Processes, c1.Processes[1])
			case "counter-reset":
				c1.Processes[1].Runtime = 1
			case "global-key":
				c1.Processes[1].PID = 800
			case "cgroup-migration":
				c1.Processes[1].CgroupID = 10
			case "new-thread":
				a.Threads[102] = Task{102, 52, "guest-burn"}
			case "gone-thread":
				delete(a.Threads, 101)
			case "thread-reuse":
				v := a.Threads[101]
				v.Start++
				a.Threads[101] = v
			case "renamed":
				v := a.Threads[101]
				v.Command = "other"
				a.Threads[101] = v
			case "bad-window":
				c1.DetailsCapturedAt = c0.DetailsCapturedAt
			case "mixed-cgroup":
				c0.Processes[1].CgroupID = 10
				c1.Processes[1].CgroupID = 10
			}
			row := measureProcess(Process{}, b, a, c0, c1, l0, l1)
			if row.CPU != nil || row.RunQLat != nil || len(row.Issues) == 0 {
				t.Fatalf("invalid totals accepted: %+v", row)
			}
		})
	}
}
func TestGuestMissingLatencyIsNotObservedZero(t *testing.T) {
	b, a, c0, c1, l0, l1 := fixture()
	row := measureProcess(Process{}, b, a, c0, c1, nil, nil)
	if row.CPU == nil || row.RunQLat != nil || len(row.Issues) == 0 {
		t.Fatal(row)
	}
	row = measureProcess(Process{}, b, a, c0, c1, l0, l0)
	if row.RunQLat == nil || row.RunQLat.Count != 0 {
		t.Fatal(row)
	}
	l1[0].Buckets[5] = 0
	row = measureProcess(Process{}, b, a, c0, c1, l0, l1)
	if row.RunQLat == nil || row.RunQLat.Count != 4 || row.RunQLatCoverage.ObservedThreads != 1 || row.RunQLatCoverage.TotalThreads != 2 || len(row.Issues) == 0 {
		t.Fatal("reset endpoint was not excluded/marked partial", row)
	}
}

type fakeCPU struct {
	a, b  probe.CPUSnapshot
	calls int
	err   error
}
type fakeIdentity struct {
	values map[uint32]probe.TaskIdentity
	err    error
	calls  int
}

func (f *fakeIdentity) TaskIdentity(pid uint32) (probe.TaskIdentity, bool, error) {
	f.calls++
	v, ok := f.values[pid]
	return v, ok, f.err
}

func (f *fakeCPU) Snapshot() (probe.CPUSnapshot, error) {
	f.calls++
	if f.calls == 1 {
		return f.a, f.err
	}
	return f.b, f.err
}

type fakeLat struct {
	a, b  []probe.RunQLatHistogram
	calls int
}

func (f *fakeLat) Snapshot() ([]probe.RunQLatHistogram, error) {
	f.calls++
	if f.calls == 1 {
		return f.a, nil
	}
	return f.b, nil
}

type fakeProc struct {
	a, b  map[uint32]ProcView
	calls int
}

func (f *fakeProc) Snapshot(string) (map[uint32]ProcView, error) {
	f.calls++
	if f.calls == 1 {
		return f.a, nil
	}
	return f.b, nil
}
func fakeHandler() *Handler {
	b, a, c0, c1, l0, l1 := fixture()
	return &Handler{Identity: Response{PodUID: "uid", KernelRelease: "6.18.56", BootID: "00112233-4455-6677-8899-aabbccddeeff", InstanceID: strings.Repeat("1", 32)}, CPU: &fakeCPU{a: c0, b: c1}, Latency: &fakeLat{a: l0, b: l1}, Proc: &fakeProc{a: map[uint32]ProcView{100: b}, b: map[uint32]ProcView{100: a}}, Scheduler: &fakeIdentity{values: map[uint32]probe.TaskIdentity{700: {CgroupID: 9, TGID: 700}, 701: {CgroupID: 9, TGID: 700}}}}
}

func TestGuestHistoricalCgroupKeysRequireScheduledIdentity(t *testing.T) {
	b, a, c0, c1, l0, l1 := fixture()
	old := c0.Processes[0]
	old.CgroupID = 8
	old.Runtime = 9000000000
	c0.Processes = append(c0.Processes, old)
	c1.Processes = append(c1.Processes, old)
	views := map[uint32]ProcView{100: b}
	identity := &fakeIdentity{values: map[uint32]probe.TaskIdentity{700: {CgroupID: 9, TGID: 700}, 701: {CgroupID: 9, TGID: 700}}}
	s0, err := scopeCPU(c0, views, identity)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := scopeCPU(c1, views, identity)
	if err != nil {
		t.Fatal(err)
	}
	row := measureProcess(Process{}, b, a, s0, s1, l0, l1)
	if row.CPU == nil || row.CPU.Percent != 50 || row.CgroupID != 9 {
		t.Fatal("historical key chosen", row)
	}
	// Missing identity is not permission to fall back to the larger old value.
	delete(identity.values, 700)
	s0, err = scopeCPU(c0, views, identity)
	if err != nil {
		t.Fatal(err)
	}
	row = measureProcess(Process{}, b, a, s0, s1, l0, l1)
	if row.CPU != nil {
		t.Fatal("identity loss became valid total")
	}
	identity.err = fmt.Errorf("map failed")
	if _, err = scopeCPU(c0, views, identity); err == nil {
		t.Fatal("map failure hidden")
	}
	if _, err = scopeCPU(c0, views, nil); err == nil {
		t.Fatal("nil identity source accepted")
	}
}
func TestGuestHTTPContract(t *testing.T) {
	for _, tc := range []struct {
		method, path, nonce string
		status              int
	}{{"POST", "/", "", 405}, {"GET", "/?duration=0s", "", 400}, {"GET", "/?duration=11s", "", 400}, {"GET", "/?sandbox=abc", "", 400}, {"GET", "/?url=http://example.com", "", 400}, {"GET", "/?limit=101", "", 400}, {"GET", "/?duration=1s&duration=1s", "", 400}, {"GET", "/?comm=1234567890123456", "", 400}, {"GET", "/?duration=100ms", "bad", 400}, {"GET", "/?duration=100ms", "", 200}} {
		h := fakeHandler()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r.Header.Set(NonceHeader, tc.nonce)
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
		if w.Code == 200 {
			var got Response
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Domain != "guest" || got.SandboxID != "" || !hexID(got.RequestID, 32) || got.Processes[0].CPU.Percent != 50 {
				t.Fatal(w.Body.String())
			}
		}
	}
	h := fakeHandler()
	h.mu.Lock()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	h.mu.Unlock()
	if w.Code != 429 {
		t.Fatal(w.Code)
	}
	h = fakeHandler()
	h.CPU.(*fakeCPU).err = fmt.Errorf("map failure")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	h = fakeHandler()
	h.Proc.(*fakeProc).b = map[uint32]ProcView{}
	got, err := h.measure(context.Background(), query{duration: time.Millisecond, limit: 20})
	if err != nil || got.Processes[0].CPU != nil || !got.Processes[0].Partial {
		t.Fatal(got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fakeHandler().measure(ctx, query{duration: time.Second, limit: 20}); err != context.Canceled {
		t.Fatal(err)
	}
}
