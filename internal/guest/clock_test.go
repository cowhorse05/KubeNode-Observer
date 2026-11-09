package guest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

var clockID = ClockIdentity{BootID: "00112233-4455-6677-8899-aabbccddeeff", TimeNamespace: "time:[123]"}

type scriptedClock struct {
	mu     sync.Mutex
	times  []int64
	index  int
	ids    int
	errAt  int
	change bool
}

func (s *scriptedClock) Now() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.index++
	if s.errAt == s.index {
		return 0, fmt.Errorf("read failed")
	}
	if s.index > len(s.times) {
		return 0, fmt.Errorf("out of timestamps")
	}
	return s.times[s.index-1], nil
}
func (s *scriptedClock) Identity() (ClockIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids++
	v := clockID
	if s.change && s.ids > 1 {
		v.TimeNamespace = "time:[456]"
	}
	return v, nil
}

func TestClockBoundsAsymmetryAndIntegerPrecision(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 1000; i++ {
		h1 := int64(1<<54) + int64(i*1000)
		offset := int64(rng.Intn(2001) - 1000)
		forward, back, process := int64(rng.Intn(100)), int64(rng.Intn(100)), int64(rng.Intn(100))
		g2 := h1 + forward + offset
		g3 := g2 + process
		h4 := h1 + forward + process + back
		s, err := EstimateClock(h1, g2, g3, h4)
		if err != nil || s.OffsetLowerNS > offset || s.OffsetUpperNS < offset || s.OffsetUpperNS-s.OffsetLowerNS != forward+back {
			t.Fatal(s, err, offset)
		}
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var out ClockSample
		if err = json.Unmarshal(b, &out); err != nil || out != s {
			t.Fatal("integer precision lost", string(b), err)
		}
		if !strings.Contains(string(b), `"hostSendNs":"`) {
			t.Fatal("NS must be strings")
		}
	}
	for _, v := range [][4]int64{{100, 1120, 1130, 160}, {10000, 20, 30, 10100}, {math.MaxInt64 - 100, math.MaxInt64 - 90, math.MaxInt64 - 80, math.MaxInt64 - 10}} {
		if _, err := EstimateClock(v[0], v[1], v[2], v[3]); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range [][4]int64{{-1, 1, 2, 3}, {3, 10, 20, 2}, {1, 20, 10, 100}, {1, 10, 1000, 100}, {1, -1, 1, 4}} {
		if _, err := EstimateClock(v[0], v[1], v[2], v[3]); err == nil {
			t.Fatal("invalid clock accepted", v)
		}
	}
}

func clockRequest(h *Relay, query string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.CalibrateHTTP(w, httptest.NewRequest("GET", ClockPath+"?sandbox="+testSID+"&samples=3"+query, nil))
	return w
}
func hostClockFixture() *scriptedClock {
	return &scriptedClock{times: []int64{1000, 1100, 2000, 2060, 3000, 3100}}
}
func replyClockFixture() *scriptedClock {
	return &scriptedClock{times: []int64{5010, 5020, 6010, 6020, 7010, 7020}}
}

func TestClockHandlerAndRelayRealHTTP(t *testing.T) {
	h, _, _ := relayFixture()
	h.clock = hostClockFixture()
	probe := &ClockHandler{PodUID: "uid", InstanceID: strings.Repeat("1", 32), Source: replyClockFixture()}
	server := httptest.NewServer(probe)
	defer server.Close()
	attachBackend(h, server)
	w := clockRequest(h, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	var result Calibration
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SandboxID != testSID || result.PodUID != "uid" || len(result.Samples) != 3 || result.SelectedSample != 1 || result.Scope != "exchange-only" || result.Samples[1].OffsetLowerNS != 3960 || result.Samples[1].OffsetUpperNS != 4010 {
		t.Fatalf("%+v", result)
	}
	seen := map[string]bool{}
	for _, s := range result.Samples {
		if !hexID(s.RequestID, 32) || seen[s.RequestID] {
			t.Fatal("nonce reused")
		}
		seen[s.RequestID] = true
	}
}

func TestClockSameKernelControl(t *testing.T) {
	h, _, _ := relayFixture()
	server := httptest.NewServer(&ClockHandler{PodUID: "uid", InstanceID: strings.Repeat("1", 32), Source: LinuxClock{}})
	defer server.Close()
	attachBackend(h, server)
	w := clockRequest(h, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result Calibration
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Host != result.Guest {
		t.Fatal("not the same clock domain")
	}
	for _, sample := range result.Samples {
		if sample.OffsetLowerNS > 0 || sample.OffsetUpperNS < 0 {
			t.Fatal("known zero offset outside interval", sample)
		}
	}
	t.Logf("same-kernel CLOCK_MONOTONIC control: zero is inside all %d intervals", len(result.Samples))
}

func TestClockDoesNotIntersectDifferentExchanges(t *testing.T) {
	h, _, _ := relayFixture()
	h.clock = hostClockFixture()
	guest := replyClockFixture()
	guest.times = []int64{5010, 5020, 7010, 7020, 9010, 9020}
	server := httptest.NewServer(&ClockHandler{PodUID: "uid", InstanceID: strings.Repeat("1", 32), Source: guest})
	defer server.Close()
	attachBackend(h, server)
	w := clockRequest(h, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result Calibration
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.SelectedSample != 1 || result.Samples[0].OffsetUpperNS >= result.Samples[1].OffsetLowerNS {
		t.Fatal("disjoint exchange-local intervals were combined", result)
	}
}

func TestClockDirectErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{{"post", 405}, {"query", 400}, {"nonce", 400}, {"missing-source", 503}, {"read", 503}, {"backwards", 503}, {"valid", 200}} {
		t.Run(tc.name, func(t *testing.T) {
			source := replyClockFixture()
			h := &ClockHandler{PodUID: "uid", InstanceID: strings.Repeat("1", 32), Source: source}
			r := httptest.NewRequest("GET", ClockPath, nil)
			switch tc.name {
			case "post":
				r.Method = "POST"
			case "query":
				r.URL.RawQuery = "url=private"
			case "nonce":
				r.Header.Set(NonceHeader, "bad")
			case "missing-source":
				h.Source = nil
			case "read":
				source.errAt = 1
			case "backwards":
				source.times = []int64{10, 1}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.status == 200 {
				var v ClockReply
				_ = json.Unmarshal(w.Body.Bytes(), &v)
				if !hexID(v.RequestID, 32) {
					t.Fatal("direct nonce missing")
				}
			}
		})
	}
}

func TestClockRejectsUpstreamAndGenerationChanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{{"identity", 409}, {"domain", 409}, {"clock", 409}, {"nonce", 409}, {"instance", 409}, {"guest-boot", 409}, {"guest-namespace", 409}, {"host-namespace", 409}, {"host-backwards", 409}, {"guest-backwards", 409}, {"generation", 409}, {"deleted", 409}, {"contradiction", 409}, {"missing-times", 502}, {"trailing", 502}, {"unknown-field", 502}, {"oversize", 502}, {"bad-json", 502}, {"bad-media", 502}, {"html-error", 502}, {"json-error", 502}, {"redirect", 502}, {"busy", 429}, {"timeout", 504}, {"host-read", 503}, {"host-receive-read", 503}} {
		t.Run(tc.name, func(t *testing.T) {
			h, topo, _ := relayFixture()
			host := hostClockFixture()
			h.clock = host
			if tc.name == "timeout" {
				h.client.Timeout = 50 * time.Millisecond
			}
			if tc.name == "host-namespace" {
				host.change = true
			}
			if tc.name == "host-backwards" {
				host.times[2] = 100
			}
			if tc.name == "host-read" {
				host.errAt = 1
			}
			if tc.name == "host-receive-read" {
				host.errAt = 2
			}
			var counter atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls := int(counter.Add(1))
				if r.URL.Path != ClockPath || r.URL.RawQuery != "" {
					t.Error("wrong request", r.URL)
				}
				v := ClockReply{Domain: "guest", Clock: ClockName, PodUID: "uid", InstanceID: strings.Repeat("1", 32), RequestID: r.Header.Get(NonceHeader), Identity: clockID, ReceivedNS: 5010 + int64(calls-1)*1000, SentNS: 5020 + int64(calls-1)*1000}
				w.Header().Set("Content-Type", "application/json")
				switch tc.name {
				case "identity":
					v.PodUID = "wrong"
				case "domain":
					v.Domain = "host"
				case "clock":
					v.Clock = "CLOCK_REALTIME"
				case "nonce":
					v.RequestID = strings.Repeat("0", 32)
				case "instance":
					if calls > 1 {
						v.InstanceID = strings.Repeat("2", 32)
					}
				case "guest-boot":
					if calls > 1 {
						v.Identity.BootID = "11112233-4455-6677-8899-aabbccddeeff"
					}
				case "guest-namespace":
					if calls > 1 {
						v.Identity.TimeNamespace = "time:[456]"
					}
				case "guest-backwards":
					if calls > 1 {
						v.ReceivedNS = 1
						v.SentNS = 2
					}
				case "generation":
					s := topo.snapshot.Sandboxes[0]
					vmm := *s.Hypervisor
					vmm.StartTimeTicks++
					s.Hypervisor = &vmm
					topo.after = &topology.Snapshot{Sandboxes: []topology.Sandbox{s}}
				case "deleted":
					topo.after = &topology.Snapshot{}
				case "contradiction":
					v.SentNS = v.ReceivedNS + 10000
				case "missing-times":
					v.ReceivedNS = 0
				case "trailing":
					_ = json.NewEncoder(w).Encode(v)
					_, _ = w.Write([]byte(`{}`))
					return
				case "unknown-field":
					b, _ := json.Marshal(v)
					_, _ = w.Write(append(b[:len(b)-1], []byte(`,"sandboxId":"forged"}`)...))
					return
				case "oversize":
					_, _ = w.Write([]byte(strings.Repeat(" ", 8193)))
					return
				case "bad-json":
					_, _ = w.Write([]byte(`{`))
					return
				case "bad-media":
					w.Header().Set("Content-Type", "application/jsonjunk")
				case "html-error":
					w.Header().Set("Content-Type", "text/html")
					w.WriteHeader(500)
					_, _ = w.Write([]byte(`<html>error</html>`))
					return
				case "json-error":
					w.WriteHeader(503)
					_, _ = w.Write([]byte(`{"code":"clock_unavailable"}`))
					return
				case "redirect":
					w.Header().Set("Location", "http://127.0.0.1:1/private")
					w.WriteHeader(302)
					return
				case "busy":
					w.WriteHeader(429)
					return
				case "timeout":
					time.Sleep(100 * time.Millisecond)
				}
				_ = json.NewEncoder(w).Encode(v)
			}))
			attachBackend(h, server)
			w := clockRequest(h, "")
			server.Close()
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.name == "busy" && w.Header().Get("Retry-After") != "1" {
				t.Fatal("missing Retry-After")
			}
		})
	}
}

func TestClockStrictQueryDisabledAndSharedGate(t *testing.T) {
	h, _, _ := relayFixture()
	for _, q := range []string{"samples=2", "samples=16", "samples=3&samples=4", "samples=x", "samples=3&url=private", "samples=3&bad=%ZZ", "samples=3;bad=1", "sandbox=short", "sandbox=" + testSID + "&sandbox=" + testSID} {
		query := q
		if !strings.HasPrefix(q, "sandbox=") {
			query = "sandbox=" + testSID + "&" + q
		}
		w := httptest.NewRecorder()
		h.CalibrateHTTP(w, httptest.NewRequest("GET", ClockPath+"?"+query, nil))
		if w.Code != 400 {
			t.Fatal(query, w.Code)
		}
	}
	h.mu.Lock()
	w := clockRequest(h, "")
	h.mu.Unlock()
	if w.Code != 429 {
		t.Fatal(w.Code)
	}
	h.port = 0
	if w = clockRequest(h, ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	h.port = 18081
	request := httptest.NewRequest("POST", ClockPath, nil)
	w = httptest.NewRecorder()
	h.CalibrateHTTP(w, request)
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := h.calibrate(ctx, target{}, 3)
	if err == nil {
		t.Fatal("cancelled calibration succeeded")
	}
}

func TestClockPostmanSimulation(t *testing.T) {
	endpoint := os.Getenv("KNO_GUEST_SIM_PROXY")
	if endpoint == "" {
		t.Skip("start local Postman simulation and set KNO_GUEST_SIM_PROXY")
	}
	proxy, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status int
	}{{"valid", 200}, {"identity", 409}, {"nonce", 409}, {"busy", 429}, {"html-error", 502}, {"json-error", 502}, {"oversize", 502}, {"redirect", 502}, {"timeout", 504}} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := relayFixture()
			h.clock = LinuxClock{}
			transport := h.client.Transport.(*http.Transport)
			transport.Proxy = http.ProxyURL(proxy)
			h.client.Transport = clockFixtureHeader{base: transport, scenario: tc.name}
			w := clockRequest(h, "")
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.status == 200 {
				var v Calibration
				if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil || len(v.Samples) != 3 {
					t.Fatal(err, w.Body.String())
				}
			}
		})
	}
}

// Only labels the owned mock fixture; responses still travel over real HTTP.
type clockFixtureHeader struct {
	base     http.RoundTripper
	scenario string
}

func (t clockFixtureHeader) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("X-Kno-Mock-Clock-Case", t.scenario)
	return t.base.RoundTrip(copy)
}
