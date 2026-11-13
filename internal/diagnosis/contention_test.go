package diagnosis

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func pointer[T any](value T) *T { return &value }

func validSummary() M10cSummary {
	quadrant := func(host bool, workers int, guest, hostP95 []float64) Quadrant {
		return Quadrant{
			HostContention:        pointer(host),
			GuestWorkers:          pointer(workers),
			Samples:               pointer(2),
			GuestRunqlatP95Us:     guest,
			HostVcpu0RunqlatP95Us: hostP95,
		}
	}
	return M10cSummary{
		Result:       "captures_valid",
		Scope:        M10cSummaryScope,
		Rounds:       2,
		CapturePairs: 8,
		Quadrants: map[string]Quadrant{
			"h0g1": quadrant(false, 1, []float64{127, 127}, []float64{127, 127}),
			"h0g8": quadrant(false, 8, []float64{1023, 2047}, []float64{127, 127}),
			"h1g1": quadrant(true, 1, []float64{127, 127}, []float64{32767, 32767}),
			"h1g8": quadrant(true, 8, []float64{32767, 32767}, []float64{32767, 32767}),
		},
		QualityWarnings: &QualityWarnings{
			PartialOrIncompleteCoverageCount:   pointer(0),
			PartialOrIncompleteCoverageWindows: []string{},
			ZeroWaitSampleCount:                pointer(0),
			ZeroWaitSampleWindows:              []string{},
		},
	}
}

func TestAnalyzeM10cFinalEvidence(t *testing.T) {
	readout, err := AnalyzeM10c(validSummary())
	if err != nil {
		t.Fatal(err)
	}
	if readout.Assessment != "complete_for_descriptive_contrasts" {
		t.Fatalf("assessment = %q", readout.Assessment)
	}
	wantVerdicts := []string{"guest_internal_signal", "host_vcpu_signal", "mixed_signal"}
	for index, want := range wantVerdicts {
		if got := readout.Contrasts[index].Verdict; got != want {
			t.Errorf("contrast %d verdict = %q, want %q", index, got, want)
		}
	}
	guest := readout.Contrasts[0].GuestP95
	if guest.Direction != "higher_in_every_round" || guest.ReferenceMedian != 127 || guest.TargetMedian != 1535 {
		t.Errorf("unexpected Guest contrast: %+v", guest)
	}
}

func TestAnalyzeM10cQualityWarningsForceInsufficientEvidence(t *testing.T) {
	summary := validSummary()
	summary.QualityWarnings.PartialOrIncompleteCoverageCount = pointer(1)
	summary.QualityWarnings.PartialOrIncompleteCoverageWindows = []string{"round-01-h0-g8"}
	readout, err := AnalyzeM10c(summary)
	if err != nil {
		t.Fatal(err)
	}
	if readout.Assessment != "insufficient_evidence" {
		t.Fatalf("assessment = %q", readout.Assessment)
	}
	for _, contrast := range readout.Contrasts {
		if contrast.Verdict != "insufficient_evidence" {
			t.Errorf("%s verdict = %q", contrast.Scenario, contrast.Verdict)
		}
	}
}

func TestAnalyzeM10cOneRoundIsNotPromotedToEvidence(t *testing.T) {
	summary := validSummary()
	summary.Rounds = 1
	summary.CapturePairs = 4
	for name, quadrant := range summary.Quadrants {
		quadrant.Samples = pointer(1)
		quadrant.GuestRunqlatP95Us = quadrant.GuestRunqlatP95Us[:1]
		quadrant.HostVcpu0RunqlatP95Us = quadrant.HostVcpu0RunqlatP95Us[:1]
		summary.Quadrants[name] = quadrant
	}
	readout, err := AnalyzeM10c(summary)
	if err != nil {
		t.Fatal(err)
	}
	if readout.Assessment != "insufficient_evidence" {
		t.Fatalf("assessment = %q", readout.Assessment)
	}
}

func TestAnalyzeM10cDoesNotHideContradictoryRounds(t *testing.T) {
	summary := validSummary()
	quadrant := summary.Quadrants["h0g8"]
	quadrant.GuestRunqlatP95Us = []float64{255, 63}
	summary.Quadrants["h0g8"] = quadrant
	readout, err := AnalyzeM10c(summary)
	if err != nil {
		t.Fatal(err)
	}
	contrast := readout.Contrasts[0]
	if contrast.GuestP95.Direction != "inconsistent_across_rounds" || contrast.Verdict != "no_consistent_guest_signal" {
		t.Fatalf("unexpected contradictory readout: %+v", contrast)
	}
}

func TestAnalyzeM10cRejectsSelfContradictoryInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*M10cSummary)
		match  string
	}{
		{name: "capture count", mutate: func(summary *M10cSummary) { summary.CapturePairs = 7 }, match: "rounds x 4"},
		{name: "missing warnings", mutate: func(summary *M10cSummary) { summary.QualityWarnings = nil }, match: "qualityWarnings"},
		{name: "warning count", mutate: func(summary *M10cSummary) {
			summary.QualityWarnings.ZeroWaitSampleCount = pointer(1)
		}, match: "warning counts"},
		{name: "quadrant metadata", mutate: func(summary *M10cSummary) {
			quadrant := summary.Quadrants["h1g1"]
			quadrant.HostContention = pointer(false)
			summary.Quadrants["h1g1"] = quadrant
		}, match: "metadata"},
		{name: "sample length", mutate: func(summary *M10cSummary) {
			quadrant := summary.Quadrants["h0g1"]
			quadrant.GuestRunqlatP95Us = quadrant.GuestRunqlatP95Us[:1]
			summary.Quadrants["h0g1"] = quadrant
		}, match: "paired sample"},
		{name: "nonfinite value", mutate: func(summary *M10cSummary) {
			quadrant := summary.Quadrants["h0g1"]
			quadrant.GuestRunqlatP95Us[0] = math.Inf(1)
			summary.Quadrants["h0g1"] = quadrant
		}, match: "invalid P95"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			summary := validSummary()
			test.mutate(&summary)
			_, err := AnalyzeM10c(summary)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("error = %v, want substring %q", err, test.match)
			}
		})
	}
}

func TestDecodeM10cSummaryAcceptsProducerFieldsAndRejectsTrailingJSON(t *testing.T) {
	data, err := json.Marshal(validSummary())
	if err != nil {
		t.Fatal(err)
	}
	withProducerField := append(append([]byte{}, data[:len(data)-1]...), []byte(`,"interpretation":"descriptive only","observedDirections":{"guestLoadRaisedGuestP95InBothHostStates":false}}`)...)
	decoded, err := DecodeM10cSummary(withProducerField)
	if err != nil {
		t.Fatalf("producer field should remain forward-compatible: %v", err)
	}
	readout, err := AnalyzeM10c(decoded)
	if err != nil || readout.Contrasts[0].Verdict != "guest_internal_signal" {
		t.Fatalf("producer direction must not override raw arrays: readout=%+v error=%v", readout, err)
	}
	if _, err := DecodeM10cSummary(append(data, []byte(` {}`)...)); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing JSON error = %v", err)
	}
}
