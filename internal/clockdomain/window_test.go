package clockdomain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type testClock struct {
	now, ids, fail  int
	change, invalid bool
}

func (s *testClock) Now() (int64, error) {
	s.now++
	if s.now == s.fail {
		return 0, fmt.Errorf("clock failed")
	}
	return 1<<54 + int64(s.now), nil
}
func (s *testClock) Identity() (Identity, error) {
	s.ids++
	id := Identity{BootID: "00112233-4455-6677-8899-aabbccddeeff", TimeNamespace: "unavailable"}
	if s.change && s.ids > 1 {
		id.TimeNamespace = "time:[2]"
	}
	if s.invalid {
		id.BootID = "bad"
	}
	return id, nil
}
func TestRecorderOptInAndActualReaderBounds(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := &testClock{}
		r, err := Begin(enabled, s)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		for b := CPUStart; b <= RunQLatEnd; b++ {
			v, err := Capture(r, b, func() (int, error) {
				calls++
				if enabled && s.now != 2*calls-1 {
					t.Fatal("clock not around actual reader", s.now)
				}
				return calls, nil
			})
			if err != nil || v != calls {
				t.Fatal(v, err)
			}
		}
		w, err := r.Finish()
		if err != nil || calls != 4 {
			t.Fatal(err, calls)
		}
		if !enabled {
			if w != nil || s.now != 0 || s.ids != 0 {
				t.Fatal("default touched clock")
			}
			continue
		}
		if s.now != 8 || s.ids != 2 || w.Validate() != nil {
			t.Fatal(w)
		}
		data, _ := json.Marshal(w)
		if !strings.Contains(string(data), `"beforeNs":"18014398509481985"`) {
			t.Fatal("integer precision lost", string(data))
		}
	}
}
func TestRecorderFailsClosed(t *testing.T) {
	for _, scenario := range []string{"clock-before", "clock-after", "identity", "invalid", "reader", "incomplete", "order", "backwards", "missing"} {
		t.Run(scenario, func(t *testing.T) {
			s := &testClock{change: scenario == "identity", invalid: scenario == "invalid"}
			if scenario == "clock-before" {
				s.fail = 1
			}
			if scenario == "clock-after" {
				s.fail = 2
			}
			r, err := Begin(true, s)
			if err == nil {
				for b := CPUStart; b <= RunQLatEnd; b++ {
					if scenario == "incomplete" && b == CPUEnd {
						break
					}
					boundary := b
					if scenario == "order" {
						boundary = RunQLatEnd
					}
					_, err = Capture(r, boundary, func() (int, error) {
						if scenario == "reader" {
							return 0, fmt.Errorf("map failed")
						}
						return 1, nil
					})
					if err != nil {
						break
					}
				}
				if err == nil {
					if scenario == "backwards" {
						r.window.End.CPU.BeforeNS = 1
					}
					if scenario == "missing" {
						r.window.End.RunQLat.AfterNS = 0
					}
					_, err = r.Finish()
				}
			}
			if err == nil {
				t.Fatal("bad window accepted")
			}
		})
	}
}
