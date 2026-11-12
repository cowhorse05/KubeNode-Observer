package diagnosis

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
)

const (
	M10cSummaryScope = "descriptive-host-x-guest-four-quadrant-summary"
	SchemaVersion    = "kno.contention-diagnosis/v1"
)

type M10cSummary struct {
	Result          string              `json:"result"`
	Scope           string              `json:"scope"`
	Rounds          int                 `json:"rounds"`
	CapturePairs    int                 `json:"capturePairs"`
	Quadrants       map[string]Quadrant `json:"quadrants"`
	QualityWarnings *QualityWarnings    `json:"qualityWarnings"`
}

type Quadrant struct {
	HostContention        *bool     `json:"hostContention"`
	GuestWorkers          *int      `json:"guestWorkers"`
	Samples               *int      `json:"samples"`
	GuestRunqlatP95Us     []float64 `json:"guestRunqlatP95Us"`
	HostVcpu0RunqlatP95Us []float64 `json:"hostVcpu0RunqlatP95Us"`
}

type QualityWarnings struct {
	PartialOrIncompleteCoverageCount   *int     `json:"partialOrIncompleteCoverageCount"`
	PartialOrIncompleteCoverageWindows []string `json:"partialOrIncompleteCoverageWindows"`
	ZeroWaitSampleCount                *int     `json:"zeroWaitSampleCount"`
	ZeroWaitSampleWindows              []string `json:"zeroWaitSampleWindows"`
}

type Readout struct {
	SchemaVersion string     `json:"schemaVersion"`
	InputScope    string     `json:"inputScope"`
	Assessment    string     `json:"assessment"`
	Rounds        int        `json:"rounds"`
	Contrasts     []Contrast `json:"contrasts"`
	Limitations   []string   `json:"limitations"`
}

type Contrast struct {
	Scenario     string `json:"scenario"`
	Reference    string `json:"reference"`
	Target       string `json:"target"`
	Verdict      string `json:"verdict"`
	GuestP95     Change `json:"guestP95"`
	HostVcpu0P95 Change `json:"hostVcpu0P95"`
}

type Change struct {
	Direction       string  `json:"direction"`
	IncreasedRounds int     `json:"increasedRounds"`
	DecreasedRounds int     `json:"decreasedRounds"`
	TiedRounds      int     `json:"tiedRounds"`
	ReferenceMedian float64 `json:"referenceMedianUs"`
	TargetMedian    float64 `json:"targetMedianUs"`
}

func DecodeM10cSummary(data []byte) (M10cSummary, error) {
	var summary M10cSummary
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&summary); err != nil {
		return M10cSummary{}, fmt.Errorf("decode M10c summary: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return M10cSummary{}, fmt.Errorf("decode M10c summary: trailing JSON value")
		}
		return M10cSummary{}, fmt.Errorf("decode M10c summary: %w", err)
	}
	return summary, nil
}

func AnalyzeM10c(summary M10cSummary) (Readout, error) {
	if err := validateSummary(summary); err != nil {
		return Readout{}, err
	}
	qualityComplete := summary.Rounds >= 2 &&
		*summary.QualityWarnings.PartialOrIncompleteCoverageCount == 0 &&
		*summary.QualityWarnings.ZeroWaitSampleCount == 0
	assessment := "complete_for_descriptive_contrasts"
	if !qualityComplete {
		assessment = "insufficient_evidence"
	}

	baseline := summary.Quadrants["h0g1"]
	guestOnly := buildContrast("guest-only", "h0g1", "h0g8", baseline, summary.Quadrants["h0g8"])
	hostOnly := buildContrast("host-only", "h0g1", "h1g1", baseline, summary.Quadrants["h1g1"])
	mixed := buildContrast("mixed", "h0g1", "h1g8", baseline, summary.Quadrants["h1g8"])
	if qualityComplete {
		guestOnly.Verdict = guestOnlyVerdict(guestOnly)
		hostOnly.Verdict = hostOnlyVerdict(hostOnly)
		mixed.Verdict = mixedVerdict(mixed)
	}

	return Readout{
		SchemaVersion: SchemaVersion,
		InputScope:    summary.Scope,
		Assessment:    assessment,
		Rounds:        summary.Rounds,
		Contrasts:     []Contrast{guestOnly, hostOnly, mixed},
		Limitations: []string{
			"Signals are paired descriptive contrasts, not automatic root-cause or causal proof.",
			"P95 values are histogram bucket bounds; they are not exact percentiles.",
			"Host and Guest clocks remain separate; this readout does not infer cross-domain overlap.",
			"Two short rounds are demonstration evidence, not a performance guarantee.",
		},
	}, nil
}

func validateSummary(summary M10cSummary) error {
	if summary.Result != "captures_valid" {
		return fmt.Errorf("summary result must be captures_valid")
	}
	if summary.Scope != M10cSummaryScope {
		return fmt.Errorf("summary scope %q is unsupported", summary.Scope)
	}
	if summary.Rounds < 1 {
		return fmt.Errorf("summary rounds must be positive")
	}
	if summary.CapturePairs != summary.Rounds*4 {
		return fmt.Errorf("capturePairs must equal rounds x 4")
	}
	if summary.QualityWarnings == nil {
		return fmt.Errorf("qualityWarnings is required")
	}
	if err := validateWarnings(*summary.QualityWarnings); err != nil {
		return err
	}
	expected := map[string]struct {
		host    bool
		workers int
	}{
		"h0g1": {host: false, workers: 1},
		"h0g8": {host: false, workers: 8},
		"h1g1": {host: true, workers: 1},
		"h1g8": {host: true, workers: 8},
	}
	for name, condition := range expected {
		quadrant, exists := summary.Quadrants[name]
		if !exists {
			return fmt.Errorf("quadrant %s is missing", name)
		}
		if quadrant.HostContention == nil || quadrant.GuestWorkers == nil ||
			*quadrant.HostContention != condition.host || *quadrant.GuestWorkers != condition.workers {
			return fmt.Errorf("quadrant %s condition metadata is inconsistent", name)
		}
		if quadrant.Samples == nil || *quadrant.Samples != summary.Rounds ||
			len(quadrant.GuestRunqlatP95Us) != summary.Rounds ||
			len(quadrant.HostVcpu0RunqlatP95Us) != summary.Rounds {
			return fmt.Errorf("quadrant %s does not contain one paired sample per round", name)
		}
		for _, values := range [][]float64{quadrant.GuestRunqlatP95Us, quadrant.HostVcpu0RunqlatP95Us} {
			for _, value := range values {
				if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					return fmt.Errorf("quadrant %s contains an invalid P95 value", name)
				}
			}
		}
	}
	return nil
}

func validateWarnings(warnings QualityWarnings) error {
	if warnings.PartialOrIncompleteCoverageCount == nil || warnings.ZeroWaitSampleCount == nil ||
		warnings.PartialOrIncompleteCoverageWindows == nil || warnings.ZeroWaitSampleWindows == nil ||
		*warnings.PartialOrIncompleteCoverageCount < 0 || *warnings.ZeroWaitSampleCount < 0 ||
		*warnings.PartialOrIncompleteCoverageCount != len(warnings.PartialOrIncompleteCoverageWindows) ||
		*warnings.ZeroWaitSampleCount != len(warnings.ZeroWaitSampleWindows) {
		return fmt.Errorf("quality warning counts contradict their window lists")
	}
	return nil
}

func buildContrast(scenario, referenceName, targetName string, reference, target Quadrant) Contrast {
	return Contrast{
		Scenario:     scenario,
		Reference:    referenceName,
		Target:       targetName,
		Verdict:      "insufficient_evidence",
		GuestP95:     compare(reference.GuestRunqlatP95Us, target.GuestRunqlatP95Us),
		HostVcpu0P95: compare(reference.HostVcpu0RunqlatP95Us, target.HostVcpu0RunqlatP95Us),
	}
}

func compare(reference, target []float64) Change {
	change := Change{ReferenceMedian: median(reference), TargetMedian: median(target)}
	for index := range reference {
		switch {
		case target[index] > reference[index]:
			change.IncreasedRounds++
		case target[index] < reference[index]:
			change.DecreasedRounds++
		default:
			change.TiedRounds++
		}
	}
	switch {
	case change.IncreasedRounds == len(reference):
		change.Direction = "higher_in_every_round"
	case change.DecreasedRounds == len(reference):
		change.Direction = "lower_in_every_round"
	case change.TiedRounds == len(reference):
		change.Direction = "unchanged_in_every_round"
	default:
		change.Direction = "inconsistent_across_rounds"
	}
	return change
}

func median(values []float64) float64 {
	copyOfValues := slices.Clone(values)
	slices.Sort(copyOfValues)
	middle := len(copyOfValues) / 2
	if len(copyOfValues)%2 == 1 {
		return copyOfValues[middle]
	}
	return (copyOfValues[middle-1] + copyOfValues[middle]) / 2
}

func guestOnlyVerdict(contrast Contrast) string {
	if contrast.GuestP95.Direction == "higher_in_every_round" && contrast.HostVcpu0P95.Direction == "unchanged_in_every_round" {
		return "guest_internal_signal"
	}
	if contrast.GuestP95.Direction == "higher_in_every_round" {
		return "guest_signal_with_host_change"
	}
	return "no_consistent_guest_signal"
}

func hostOnlyVerdict(contrast Contrast) string {
	if contrast.HostVcpu0P95.Direction == "higher_in_every_round" && contrast.GuestP95.Direction == "unchanged_in_every_round" {
		return "host_vcpu_signal"
	}
	if contrast.HostVcpu0P95.Direction == "higher_in_every_round" {
		return "host_signal_with_guest_change"
	}
	return "no_consistent_host_signal"
}

func mixedVerdict(contrast Contrast) string {
	guestHigher := contrast.GuestP95.Direction == "higher_in_every_round"
	hostHigher := contrast.HostVcpu0P95.Direction == "higher_in_every_round"
	switch {
	case guestHigher && hostHigher:
		return "mixed_signal"
	case guestHigher:
		return "guest_only_signal"
	case hostHigher:
		return "host_only_signal"
	default:
		return "no_consistent_mixed_signal"
	}
}
