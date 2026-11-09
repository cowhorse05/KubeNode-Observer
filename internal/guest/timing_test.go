package guest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/cowhorse05/KubeNode-Observer/internal/clockdomain"
)

func timingClock() *scriptedClock {
	return &scriptedClock{times: []int64{10, 11, 12, 13, 20, 21, 22, 23}}
}
func TestGuestTimingOptInAndFailures(t *testing.T) {
	for _, tc := range []struct {
		query, fault string
		status       int
	}{
		{"", "", 200}, {"&timing=false", "", 200}, {"&timing=true", "", 200},
		{"&timing=1", "", 400}, {"&timing=True", "", 400}, {"&timing=", "", 400},
		{"&timing=true&timing=false", "", 400}, {"&timing=%ZZ", "", 400},
		{"&timing=true", "clock", 503}, {"&timing=true", "identity", 503}, {"&timing=true", "boot", 503},
	} {
		t.Run(tc.query+tc.fault, func(t *testing.T) {
			h := fakeHandler()
			s := timingClock()
			h.Clock = s
			if tc.fault == "clock" {
				s.errAt = 3
			}
			if tc.fault == "identity" {
				s.change = true
			}
			if tc.fault == "boot" {
				h.Identity.BootID = "different"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/?duration=100ms"+tc.query, nil))
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.status != 200 {
				return
			}
			var v Response
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if tc.query == "&timing=true" {
				if v.Timing == nil || v.Timing.Validate() != nil || v.Timing.Start.CPU.BeforeNS != 10 {
					t.Fatal(v.Timing)
				}
			} else if v.Timing != nil || s.index != 0 || s.ids != 0 {
				t.Fatal("default read clock")
			}
			if h.CPU.(*fakeCPU).calls != 2 || h.Latency.(*fakeLat).calls != 2 {
				t.Fatal("snapshot count changed")
			}
		})
	}
}
func validTiming() *clockdomain.Window {
	return &clockdomain.Window{Clock: ClockName, Identity: clockID,
		Start: clockdomain.Snapshots{CPU: clockdomain.Span{BeforeNS: 1, AfterNS: 2}, RunQLat: clockdomain.Span{BeforeNS: 3, AfterNS: 4}},
		End:   clockdomain.Snapshots{CPU: clockdomain.Span{BeforeNS: 5, AfterNS: 6}, RunQLat: clockdomain.Span{BeforeNS: 7, AfterNS: 8}}}
}
func TestRelayTimingForwardAndValidation(t *testing.T) {
	for _, fault := range []string{"valid", "missing", "backwards", "boot", "unsolicited"} {
		t.Run(fault, func(t *testing.T) {
			h, _, _ := relayFixture()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested := fault != "unsolicited"
				if (r.URL.Query().Get("timing") == "true") != requested {
					t.Error("timing not forwarded correctly")
				}
				v := validSample(r.Header.Get(NonceHeader))
				v.Timing = validTiming()
				switch fault {
				case "missing":
					v.Timing = nil
				case "backwards":
					v.Timing.End.CPU.BeforeNS = 1
				case "boot":
					v.Timing.Identity.BootID = "00112233-4455-6677-8899-aabbccddeefa"
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(v)
			}))
			defer server.Close()
			attachBackend(h, server)
			path := "/?sandbox=" + testSID + "&duration=100ms"
			if fault != "unsolicited" {
				path += "&timing=true"
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			want := 502
			if fault == "valid" {
				want = 200
			}
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
func TestTimingPostmanSimulation(t *testing.T) {
	endpoint := os.Getenv("KNO_GUEST_SIM_PROXY")
	if endpoint == "" {
		t.Skip("requires local Postman simulation")
	}
	proxy, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		comm   string
		status int
	}{
		{"valid", 200}, {"timing-missing", 502}, {"timing-order", 502}, {"timing-boot", 502},
	} {
		t.Run(tc.comm, func(t *testing.T) {
			h, _, _ := relayFixture()
			h.client.Transport.(*http.Transport).Proxy = http.ProxyURL(proxy)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", fmt.Sprintf("/?sandbox=%s&duration=100ms&timing=true&comm=%s", testSID, tc.comm), nil))
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}
