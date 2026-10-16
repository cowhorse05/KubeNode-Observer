package probe

import "testing"

func TestCPURuntimeCommand(t *testing.T) {
	runtime := CPURuntime{Comm: [cpuCommLen]byte{'w', 'o', 'r', 'k', 0, 'x'}}
	if got := runtime.Command(); got != "work" {
		t.Fatalf("Command() = %q", got)
	}
}
