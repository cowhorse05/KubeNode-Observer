package host

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/cowhorse05/KubeNode-Observer/internal/clockdomain"
)

type timingSource struct {
	reads, ids   int
	fail, change bool
}

func (s *timingSource) Now() (int64, error) {
	s.reads++
	if s.fail {
		return 0, fmt.Errorf("clock failed")
	}
	return int64(s.reads), nil
}
func (s *timingSource) Identity() (clockdomain.Identity, error) {
	s.ids++
	ns := "time:[1]"
	if s.change && s.ids > 1 {
		ns = "time:[2]"
	}
	return clockdomain.Identity{BootID: "00112233-4455-6677-8899-aabbccddeeff", TimeNamespace: ns}, nil
}
func TestHostTimingOptInAndFailures(t *testing.T) {
	for _, tc := range []struct {
		query, fault string
		status       int
	}{
		{"", "", 200}, {"&timing=false", "", 200}, {"&timing=true", "", 200}, {"&timing=true&pod=absent", "empty", 200},
		{"&timing=1", "", 400}, {"&timing=", "", 400}, {"&timing=True", "", 400},
		{"&timing=true&timing=false", "", 400}, {"&timing=%ZZ", "", 400}, {"&timing=true;bad", "", 400},
		{"&timing=true", "clock", 503}, {"&timing=true", "identity", 503},
	} {
		t.Run(tc.query+tc.fault, func(t *testing.T) {
			h, _ := fakeHandler()
			s := &timingSource{fail: tc.fault == "clock", change: tc.fault == "identity"}
			h.clock = s
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
				if v.Timing == nil || v.Timing.Validate() != nil || s.reads != 8 || s.ids != 2 {
					t.Fatal(v.Timing)
				}
			} else if v.Timing != nil || s.reads != 0 || s.ids != 0 {
				t.Fatal("unrequested/unmeasured timing")
			}
			wantCalls := 2
			if tc.fault == "empty" {
				wantCalls = 0
			}
			if h.cpu.(*fakeCPU).calls != wantCalls || h.latency.(*fakeLatency).calls != wantCalls {
				t.Fatal("map count changed")
			}
		})
	}
}
