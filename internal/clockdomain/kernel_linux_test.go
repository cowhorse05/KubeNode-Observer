package clockdomain

import (
	"os"
	"testing"

	"github.com/cilium/ebpf/rlimit"
)

// Explicit privileged integration test; ordinary CI does not claim kernel access.
func TestKernelClockBracketAndLifecycle(t *testing.T) {
	if os.Getenv("KNO_CLOCK_BPF_TEST") != "1" {
		t.Skip("set KNO_CLOCK_BPF_TEST=1 with BPF privilege")
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		k, err := NewKernelClock()
		if err != nil {
			t.Fatal(err)
		}
		before, e0 := (LinuxClock{}).Now()
		kernel, e1 := k.Now()
		after, e2 := (LinuxClock{}).Now()
		k.Close()
		if e0 != nil || e1 != nil || e2 != nil || before > kernel || kernel > after {
			t.Fatal("requires initial/zero-offset time namespace", before, kernel, after, e0, e1, e2)
		}
		if k.program.FD() != -1 || k.value.FD() != -1 {
			t.Fatal("diagnostic handles leaked")
		}
	}
}
