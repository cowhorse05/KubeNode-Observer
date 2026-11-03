package topology

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// KataMonitor only relays one fixed metrics endpoint for a current local CRI
// sandbox. It does not relay profiling/debug endpoints or accept target URLs.
type KataMonitor struct {
	endpoint string
	store    *Store
	client   *http.Client
	mu       sync.Mutex
}

func NewKataMonitor(endpoint string, store *Store) (*KataMonitor, error) {
	m := &KataMonitor{store: store, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if endpoint == "" {
		return m, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse Kata Monitor endpoint: %w", err)
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "http" || ip == nil || !ip.IsLoopback() || u.Port() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("kata-monitor must be an http literal loopback IP:port without path/query")
	}
	m.endpoint = strings.TrimRight(endpoint, "/")
	return m, nil
}

func (m *KataMonitor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if m.endpoint == "" {
		http.Error(w, "Kata Monitor is not configured", http.StatusServiceUnavailable)
		return
	}
	id := r.URL.Query().Get("sandbox")
	if len(id) != 64 || strings.Trim(id, "0123456789abcdef") != "" {
		http.Error(w, "full lowercase hex sandbox ID required", http.StatusBadRequest)
		return
	}
	if !m.store.HasSandbox(id) {
		http.Error(w, "current local Kata sandbox not found", http.StatusNotFound)
		return
	}
	if !m.mu.TryLock() {
		http.Error(w, "Kata metrics request already running", http.StatusTooManyRequests)
		return
	}
	defer m.mu.Unlock()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, m.endpoint+"/metrics?"+url.Values{"sandbox": {id}}.Encode(), nil)
	if err != nil {
		http.Error(w, "invalid monitor request", http.StatusInternalServerError)
		return
	}
	response, err := m.client.Do(req)
	if err != nil {
		http.Error(w, "Kata Monitor unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("Kata Monitor returned HTTP %d", response.StatusCode), http.StatusBadGateway)
		return
	}
	const maxSize = 8 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxSize+1))
	if err != nil || len(body) > maxSize {
		http.Error(w, "Kata Monitor metrics read failed or exceeded 8 MiB", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write(body)
}
