package cpu

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAccuracyReferenceBracketAndSignedError(t *testing.T) {
	start := time.Unix(100, 0)
	response, err := summarizeAccuracy(AccuracyResponse{ObservedRuntimeNS: 900000, Samples: []AccuracySample{
		{At: start, ReferenceBeforeUS: 100, ReferenceAfterUS: 110, ThrottledUS: 2},
		{At: start.Add(time.Second), ReferenceBeforeUS: 1100, ReferenceAfterUS: 1110, ThrottledUS: 7},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response.ReferenceLowerNS != 990000 || response.ReferenceUpperNS != 1010000 || response.WindowSeconds != 1 || response.ThrottledUS != 5 {
		t.Fatalf("window: %+v", response)
	}
	if response.SignedErrorPercent < -10.001 || response.SignedErrorPercent > -9.999 || response.AbsoluteErrorPercent < 9.999 || response.ReferenceSkewPercent != 2 {
		t.Fatalf("error: %+v", response)
	}
}

func TestAccuracyRejectsZeroReference(t *testing.T) {
	_, err := summarizeAccuracy(AccuracyResponse{Samples: []AccuracySample{{ReferenceBeforeUS: 1, ReferenceAfterUS: 1}, {ReferenceBeforeUS: 1, ReferenceAfterUS: 1}}})
	if err == nil {
		t.Fatal("zero usage must not report 0% error")
	}
}

func TestRuntimeDeltaRetainsCollectorSemantics(t *testing.T) {
	for _, test := range []struct {
		current, old uint64
		existed      bool
		want         uint64
	}{
		{90, 0, false, 90}, {120, 90, true, 30}, {10, 90, true, 10},
	} {
		if got := runtimeDelta(test.current, test.old, test.existed); got != test.want {
			t.Fatalf("%+v: got %d", test, got)
		}
	}
}

func TestAccuracyReportsMissingNewAndResetKeys(t *testing.T) {
	a, b, c, d := runtimeKey{42, 1, 1}, runtimeKey{42, 2, 2}, runtimeKey{42, 3, 3}, runtimeKey{42, 4, 4}
	previous := map[runtimeKey]uint64{a: 100, b: 200, c: 300}
	current := map[runtimeKey]uint64{a: 150, c: 10, d: 80}
	var sample AccuracySample
	applyAccuracyDelta(&sample, current, previous)
	if sample.ObservedRuntimeNS != 140 || sample.NewKeys != 1 || sample.MissingKeys != 1 || sample.ResetKeys != 1 {
		t.Fatalf("transition: %+v", sample)
	}
}

func TestCPUStatRejectsReusedIdentityAndMalformedUsage(t *testing.T) {
	path := t.TempDir()
	var stat unix.Stat_t
	if err := unix.Stat(path, &stat); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "cpu.stat"), []byte("usage_usec 500\nthrottled_usec 12\n"), 0600); err != nil {
		t.Fatal(err)
	}
	usage, throttled, err := readCPUStat(path, stat.Ino)
	if err != nil || usage != 500 || throttled != 12 {
		t.Fatalf("got %d %d %v", usage, throttled, err)
	}
	if _, _, err := readCPUStat(path, stat.Ino+1); err == nil {
		t.Fatal("reused cgroup accepted")
	}
	if err := os.WriteFile(filepath.Join(path, "cpu.stat"), []byte("usage_usec invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readCPUStat(path, stat.Ino); err == nil {
		t.Fatal("malformed cpu.stat accepted")
	}
}

func TestAccuracyCancellationAndValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := measureAccuracy(ctx, nil, "", 1, time.Second, time.Second); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
	h := NewAccuracyHandler(nil, fakeResolver{}, t.TempDir())
	for _, path := range []string{"/api/v1/cpu/accuracy", "/api/v1/cpu/accuracy?cgroupId=42&duration=1h&interval=1s"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 400 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}
