package main

import (
	"bytes"
	"strings"
	"testing"
)

const validDiagnosisSummary = `{
  "result":"captures_valid",
  "scope":"descriptive-host-x-guest-four-quadrant-summary",
  "rounds":2,
  "capturePairs":8,
  "qualityWarnings":{"partialOrIncompleteCoverageCount":0,"partialOrIncompleteCoverageWindows":[],"zeroWaitSampleCount":0,"zeroWaitSampleWindows":[]},
  "quadrants":{
    "h0g1":{"hostContention":false,"guestWorkers":1,"samples":2,"guestRunqlatP95Us":[127,127],"hostVcpu0RunqlatP95Us":[127,127]},
    "h0g8":{"hostContention":false,"guestWorkers":8,"samples":2,"guestRunqlatP95Us":[1023,2047],"hostVcpu0RunqlatP95Us":[127,127]},
    "h1g1":{"hostContention":true,"guestWorkers":1,"samples":2,"guestRunqlatP95Us":[127,127],"hostVcpu0RunqlatP95Us":[32767,32767]},
    "h1g8":{"hostContention":true,"guestWorkers":8,"samples":2,"guestRunqlatP95Us":[32767,32767],"hostVcpu0RunqlatP95Us":[32767,32767]}
  }
}`

func TestDiagnoseContentionFromStdin(t *testing.T) {
	command := newDiagnoseCommand()
	var output bytes.Buffer
	command.SetIn(strings.NewReader(validDiagnosisSummary))
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"contention", "--summary", "-"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"complete_for_descriptive_contrasts", "guest_internal_signal", "host_vcpu_signal", "mixed_signal", "not automatic root-cause"} {
		if !strings.Contains(output.String(), expected) {
			t.Errorf("output missing %q:\n%s", expected, output.String())
		}
	}
}

func TestDiagnoseContentionJSON(t *testing.T) {
	command := newDiagnoseCommand()
	var output bytes.Buffer
	command.SetIn(strings.NewReader(validDiagnosisSummary))
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"contention", "--summary", "-", "--output", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"schemaVersion": "kno.contention-diagnosis/v1"`) {
		t.Fatalf("unexpected JSON output:\n%s", output.String())
	}
}

func TestDiagnoseContentionRejectsUnknownOutput(t *testing.T) {
	command := newDiagnoseCommand()
	command.SetIn(strings.NewReader(validDiagnosisSummary))
	command.SetArgs([]string{"contention", "--summary", "-", "--output", "yaml"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "text or json") {
		t.Fatalf("error = %v", err)
	}
}

func TestReadDiagnosisInputLimit(t *testing.T) {
	_, err := readDiagnosisInput("-", strings.NewReader(strings.Repeat("x", maxDiagnosisInputBytes+1)))
	if err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("error = %v", err)
	}
}
