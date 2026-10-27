package topology

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKataMonitorValidationAndRelay(t *testing.T) {
	id := strings.Repeat("a", 64)
	store := NewStore(nil)
	store.snapshot.Store(&Snapshot{Sandboxes: []Sandbox{{ID: id}}})
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/metrics" || r.URL.Query().Get("sandbox") != id || len(r.URL.Query()) != 1 {
			t.Errorf("unsafe target: %s", r.URL)
		}
		_, _ = w.Write([]byte("kata_test{sandbox_id=\"" + id + "\"} 1\n"))
	}))
	defer upstream.Close()
	m, err := NewKataMonitor(upstream.URL, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, query string
		code          int
	}{
		{"GET", "?sandbox=" + id + "&target=http://evil&path=/debug/pprof", 200},
		{"GET", "?sandbox=" + strings.Repeat("b", 64), 404},
		{"GET", "?sandbox=../x", 400},
		{"POST", "?sandbox=" + id, 405},
	} {
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/v1/kata/metrics"+tc.query, nil))
		if w.Code != tc.code {
			t.Fatalf("%s: got %d", tc.query, w.Code)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	m.mu.Lock()
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/?sandbox="+id, nil))
	m.mu.Unlock()
	if w.Code != 429 {
		t.Fatal(w.Code)
	}
	for _, endpoint := range []string{"http://localhost:8090", "http://10.0.0.1:8090", "https://127.0.0.1:8090", "http://127.0.0.1:8090/debug", "http://127.0.0.1:8090?x=y", "http://user@127.0.0.1:8090"} {
		if _, err := NewKataMonitor(endpoint, store); err == nil {
			t.Fatal("unsafe endpoint accepted: " + endpoint)
		}
	}
}

func TestKataMonitorDisabledAndUpstreamFailure(t *testing.T) {
	id := strings.Repeat("a", 64)
	store := NewStore(nil)
	store.snapshot.Store(&Snapshot{Sandboxes: []Sandbox{{ID: id}}})
	m, _ := NewKataMonitor("", store)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://untrusted/", 302) }))
	defer upstream.Close()
	m, _ = NewKataMonitor(upstream.URL, store)
	w = httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/?sandbox="+id, nil))
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
}
