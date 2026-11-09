package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

var testSID = strings.Repeat("a", 64)

type fakeTopology struct {
	snapshot *topology.Snapshot
	calls    int
	after    *topology.Snapshot
	err      error
}

func (f *fakeTopology) CurrentSnapshot() (*topology.Snapshot, error) {
	f.calls++
	if f.calls > 1 && f.after != nil {
		return f.after, f.err
	}
	return f.snapshot, f.err
}

type fakeResolver struct {
	pod   topology.PodInfo
	ready bool
}

func (f *fakeResolver) Ready() bool                                 { return f.ready }
func (f *fakeResolver) Pod(string, string) (topology.PodInfo, bool) { return f.pod, true }
func (f *fakeResolver) Node(string) (topology.NodeInfo, bool)       { return topology.NodeInfo{}, false }
func relayFixture() (*Relay, *fakeTopology, *fakeResolver) {
	s := &fakeTopology{snapshot: &topology.Snapshot{Sandboxes: []topology.Sandbox{{ID: testSID, PodUID: "uid", Namespace: "ns", PodName: "pod", Hypervisor: &topology.HostProcess{PID: 100, StartTimeTicks: 55, CgroupID: 9}}}}}
	r := &fakeResolver{ready: true, pod: topology.PodInfo{UID: "uid", RuntimeUID: "uid", Phase: "Running", IP: "192.0.2.10", GuestProbe: true}}
	h, _ := NewRelay(s, r, 18081)
	return h, s, r
}
func validSample(nonce string) Response {
	return Response{Domain: "guest", PodUID: "uid", KernelRelease: "6.18.56", BootID: "00112233-4455-6677-8899-aabbccddeeff", InstanceID: strings.Repeat("1", 32), RequestID: nonce, StartedAt: time.Now().UTC(), DurationSeconds: .1, Processes: []Process{}}
}
func attachBackend(h *Relay, server *httptest.Server) {
	address := strings.TrimPrefix(server.URL, "http://")
	h.client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
}
func relayRequest(h *Relay, comm string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", Path+"?sandbox="+testSID+"&duration=100ms&comm="+comm, nil))
	return w
}

func TestRelayRealHTTPAndFailureShapes(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
	}{{"valid", 200}, {"busy", 429}, {"json-error", 502}, {"html-error", 502}, {"redirect", 502}, {"identity", 409}, {"domain", 409}, {"nonce", 409}, {"forged-sandbox", 409}, {"oversize", 502}, {"invalid-json", 502}, {"invalid-cpu", 502}, {"timeout", 504}, {"deleted", 409}, {"generation", 409}, {"ip-reuse", 409}} {
		t.Run(scenario.name, func(t *testing.T) {
			h, topo, resolver := relayFixture()
			h.client.Timeout = 100 * time.Millisecond
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != Path || r.URL.Query().Get("sandbox") != "" || !hexID(r.Header.Get(NonceHeader), 32) {
					t.Error("wrong upstream request", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				value := validSample(r.Header.Get(NonceHeader))
				switch scenario.name {
				case "busy":
					w.WriteHeader(429)
					return
				case "json-error":
					w.WriteHeader(503)
					_, _ = w.Write([]byte(`{"code":"unavailable"}`))
					return
				case "html-error":
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(500)
					_, _ = w.Write([]byte("<html>failed</html>"))
					return
				case "redirect":
					w.Header().Set("Location", "http://127.0.0.1:1/private")
					w.WriteHeader(302)
					return
				case "identity":
					value.PodUID = "wrong"
				case "domain":
					value.Domain = "host"
				case "nonce":
					value.RequestID = strings.Repeat("0", 32)
				case "forged-sandbox":
					value.SandboxID = testSID
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat(" ", MaxResponseBytes+1)))
					return
				case "invalid-json":
					_, _ = w.Write([]byte("{"))
					return
				case "invalid-cpu":
					value.TotalItems = 1
					value.Processes = []Process{{NamespacePID: 1, StartTimeTicks: 50, CPU: &CPU{Percent: 100, WindowSeconds: 1}}}
				case "timeout":
					time.Sleep(200 * time.Millisecond)
				case "deleted":
					topo.after = &topology.Snapshot{}
				case "generation":
					changed := *topo.snapshot.Sandboxes[0].Hypervisor
					changed.StartTimeTicks++
					s := topo.snapshot.Sandboxes[0]
					s.Hypervisor = &changed
					topo.after = &topology.Snapshot{Sandboxes: []topology.Sandbox{s}}
				case "ip-reuse":
					resolver.pod.IP = "192.0.2.11"
				}
				_ = json.NewEncoder(w).Encode(value)
			}))
			defer server.Close()
			attachBackend(h, server)
			w := relayRequest(h, "valid")
			if w.Code != scenario.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if calls.Load() != 1 {
				t.Fatal("redirect followed or no upstream request", calls.Load())
			}
			if scenario.status == 200 {
				var s Response
				_ = json.Unmarshal(w.Body.Bytes(), &s)
				if s.SandboxID != testSID || s.PodUID != "uid" || s.Namespace != "ns" || s.Domain != "guest" {
					t.Fatal(s)
				}
			}
		})
	}
	h, _, _ := relayFixture()
	h.client.Transport.(*http.Transport).DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, fmt.Errorf("connection refused") }
	if w := relayRequest(h, "valid"); w.Code != 502 {
		t.Fatal(w.Code)
	}
}
func TestRelayFailClosedBeforeDial(t *testing.T) {
	for _, scenario := range []string{"disabled", "unready", "stale", "unknown", "no-vmm", "issues", "uid", "static", "not-running", "not-opted", "hostnetwork", "loopback", "link-local", "multicast", "dns"} {
		t.Run(scenario, func(t *testing.T) {
			h, s, r := relayFixture()
			switch scenario {
			case "disabled":
				h.port = 0
			case "unready":
				r.ready = false
			case "stale":
				s.err = fmt.Errorf("stale")
			case "unknown":
				s.snapshot.Sandboxes = nil
			case "no-vmm":
				s.snapshot.Sandboxes[0].Hypervisor = nil
			case "issues":
				s.snapshot.Sandboxes[0].Issues = []string{"ambiguous"}
			case "uid":
				r.pod.UID = "new"
			case "static":
				r.pod.Static = true
			case "not-running":
				r.pod.Phase = "Pending"
			case "not-opted":
				r.pod.GuestProbe = false
			case "hostnetwork":
				r.pod.HostNetwork = true
			case "loopback":
				r.pod.IP = "127.0.0.1"
			case "link-local":
				r.pod.IP = "169.254.169.254"
			case "multicast":
				r.pod.IP = "224.0.0.1"
			case "dns":
				r.pod.IP = "service.internal"
			}
			h.client.Transport.(*http.Transport).DialContext = func(context.Context, string, string) (net.Conn, error) {
				t.Error("unexpected upstream dial")
				return nil, fmt.Errorf("must not dial")
			}
			w := relayRequest(h, "")
			if w.Code == 200 {
				t.Fatal(scenario)
			}
		})
	}
	h, _, _ := relayFixture()
	h.mu.Lock()
	w := relayRequest(h, "")
	h.mu.Unlock()
	if w.Code != 429 {
		t.Fatal(w.Code)
	}
	for _, query := range []string{"sandbox=short", "sandbox=" + testSID + "&url=http://127.0.0.1", "sandbox=" + testSID + "&limit=0"} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/?"+query, nil))
		if w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	if _, err := NewRelay(nil, nil, 65536); err == nil {
		t.Fatal("bad port accepted")
	}
}

// Optional real production Relay HTTP calls through the local Postman proxy.
// No real Kubernetes, kernel counters or Pod network is claimed by this test.
func TestRelayPostmanSimulation(t *testing.T) {
	endpoint := os.Getenv("KNO_GUEST_SIM_PROXY")
	if endpoint == "" {
		t.Skip("set KNO_GUEST_SIM_PROXY to the running local Postman simulation proxy")
	}
	proxy, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		comm   string
		status int
	}{{"valid", 200}, {"busy", 429}, {"json-error", 502}, {"html-error", 502}, {"identity", 409}, {"nonce", 409}, {"oversize", 502}, {"redirect", 502}, {"timeout", 504}} {
		t.Run(tc.comm, func(t *testing.T) {
			h, _, _ := relayFixture()
			h.client.Transport.(*http.Transport).Proxy = http.ProxyURL(proxy)
			w := relayRequest(h, tc.comm)
			if w.Code != tc.status {
				t.Fatal(tc.comm, w.Code, w.Body.String())
			}
		})
	}
}
