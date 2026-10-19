package metrics

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cowhorse05/KubeNode-Observer/internal/agent"
	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
)

type fakeHealth struct{ stats agent.HealthStats }

func (f fakeHealth) Stats() agent.HealthStats { return f.stats }

type fakeMaps struct{}

func (fakeMaps) MapStats() ([]probe.BPFMapStats, error) {
	return []probe.BPFMapStats{{Name: "cpu_runtime", Type: "LRUHash", Entries: 7, MaxEntries: 4096, Memlock: 65536}}, nil
}

func TestHandler(t *testing.T) {
	procRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(procRoot, "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "self", "schedstat"), []byte("2500000000 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procRoot, "self", "statm"), []byte("100 10 0 0 0 0 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	NewHandler(fakeHealth{agent.HealthStats{EventsSeen: 3, EventsLost: 1}}, []probe.MapStatsSource{fakeMaps{}}, procRoot).ServeHTTP(recorder, request)
	body := recorder.Body.String()
	for _, expected := range []string{
		"kno_agent_cpu_seconds_total 2.5",
		"kno_agent_events_seen_total 3",
		"kno_agent_events_lost_total 1",
		"kno_agent_bpf_map_entries{map=\"cpu_runtime\",type=\"LRUHash\"} 7",
		"kno_agent_bpf_map_memlock_bytes{map=\"cpu_runtime\",type=\"LRUHash\"} 65536",
		"kno_agent_metrics_collection_errors 0",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response does not contain %q:\n%s", expected, body)
		}
	}
}

func TestHandlerRejectsNonGET(t *testing.T) {
	recorder := httptest.NewRecorder()
	NewHandler(fakeHealth{}, nil, t.TempDir()).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
}
