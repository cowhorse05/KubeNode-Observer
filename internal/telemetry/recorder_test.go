package telemetry

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRecorderConcurrent(t *testing.T) {
	r := &Recorder{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.record(CPUScan, 300*time.Millisecond)
				_ = r.Snapshot()
			}
		}()
	}
	wg.Wait()
	s := r.Snapshot()["cpu.scan"]
	if s.Count != 800 || s.TotalNS != uint64(800*300*time.Millisecond) || s.Over100MS != 800 || s.Over250MS != 800 || s.Over1S != 0 {
		t.Fatalf("unexpected stage: %+v", s)
	}
	copy := r.Snapshot()
	delete(copy, "cpu.scan")
	if r.Snapshot()["cpu.scan"].Count != 800 {
		t.Fatal("snapshot aliases storage")
	}
	r.record(Operation(999), time.Second)
	var disabled *Recorder
	disabled.Observe(CPUScan, time.Now())
}

func TestDiagnosticsCgroup(t *testing.T) {
	root := t.TempDir()
	proc := filepath.Join(root, "proc")
	cgroup := filepath.Join(root, "cgroup")
	for name, content := range map[string]string{
		filepath.Join(proc, "self", "cgroup"):                   "0::/pod/agent\n",
		filepath.Join(cgroup, "pod", "agent", "cpu.max"):        "20000 100000\n",
		filepath.Join(cgroup, "pod", "agent", "cpu.stat"):       "usage_usec 123\nnr_throttled 4\nthrottled_usec 90\n",
		filepath.Join(proc, "self", "statm"):                    "200 10 0 0 0 0 0\n",
		filepath.Join(cgroup, "pod", "agent", "memory.current"): "12345\n",
		filepath.Join(cgroup, "pod", "agent", "memory.max"):     "134217728\n",
		filepath.Join(cgroup, "pod", "agent", "memory.stat"):    "anon 100\nfile 20\nkernel 30\n",
		filepath.Join(cgroup, "pod", "agent", "memory.events"):  "oom 0\noom_kill 0\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	h := Handler(&Recorder{}, proc, cgroup)
	h.ServeHTTP(w, httptest.NewRequest("GET", "/diagnostics", nil))
	var result Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.CPUMax != "20000 100000" || result.CPUStat["usage_usec"] != 123 || result.CgroupPath != "/pod/agent" {
		t.Fatalf("unexpected diagnostics: %+v", result)
	}
	if result.MemoryError != "" || result.Memory == nil || result.Memory.Current != 12345 || result.RSS != uint64(10*os.Getpagesize()) || result.RSSError != "" || result.GoMemoryLimit <= 0 {
		t.Fatalf("memory diagnostics: %+v", result)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/diagnostics", nil))
	if w.Code != 405 {
		t.Fatalf("status %d", w.Code)
	}
	if err := os.WriteFile(filepath.Join(proc, "self", "cgroup"), []byte("0::/../../escape\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readSelfCgroup(proc, cgroup); err == nil {
		t.Fatal("accepted traversal")
	}
}
