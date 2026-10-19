package probe

import "testing"

func TestRunQLatHistogramCommand(t *testing.T) {
	histogram := RunQLatHistogram{Comm: [taskCommLen]byte{'w', 'o', 'r', 'k', 0, 'x'}}
	if got := histogram.Command(); got != "work" {
		t.Fatalf("Command() = %q", got)
	}
}
