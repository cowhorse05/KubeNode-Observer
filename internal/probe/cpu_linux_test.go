package probe

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
	"github.com/cowhorse05/KubeNode-Observer/internal/bpfgen"
)

// Deliberately do not poll while children run: a bounded process map cannot
// promise complete container accounting once processes outlive its retention.
func TestCPURuntimeRetentionKernel(t *testing.T) {
	if os.Getenv("KNO_BPF_TEST") != "1" {
		t.Skip("set KNO_BPF_TEST=1 with BPF privileges")
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatal(err)
	}
	for _, capacity := range []uint32{8192, 16} {
		spec, err := bpfgen.LoadCPU()
		if err != nil {
			t.Fatal(err)
		}
		spec.Maps["cpu_runtime"].MaxEntries = capacity
		var objects bpfgen.CPUObjects
		if err := spec.LoadAndAssign(&objects, nil); err != nil {
			t.Fatal(err)
		}
		attached, err := link.AttachRawTracepoint(link.RawTracepointOptions{Name: "sched_switch", Program: objects.TraceCpuRuntime})
		if err != nil {
			objects.Close()
			t.Fatal(err)
		}
		childPath := filepath.Join(t.TempDir(), "kno-cpu-child")
		if err := os.Symlink("/bin/true", childPath); err != nil {
			t.Fatal(err)
		}
		var reference uint64
		for i := 0; i < 128; i++ {
			child := exec.Command(childPath)
			if err := child.Run(); err != nil {
				attached.Close()
				objects.Close()
				t.Fatal(err)
			}
			usage := child.ProcessState.SysUsage().(*syscall.Rusage)
			reference += uint64(usage.Utime.Nano() + usage.Stime.Nano())
		}
		// Freeze writers before inspecting retention. The tiny map otherwise
		// keeps evicting keys for unrelated host tasks while we iterate it.
		if err := attached.Close(); err != nil {
			objects.Close()
			t.Fatal(err)
		}
		var key cpuKey
		var value cpuValue
		var observed uint64
		retained := 0
		iterator := objects.CpuRuntime.Iterate()
		for iterator.Next(&key, &value) {
			// BPF uses initial-namespace PIDs; WSL's getpid may use a nested
			// namespace. Match our dedicated executable comm, not userspace PID.
			if (CPURuntime{Comm: value.Comm}).Command() == "kno-cpu-child" {
				observed += value.Runtime
				retained++
			}
		}
		err = iterator.Err()
		objects.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("capacity=%d children=128 retained=%d runtime=%dns wait4_CPU=%dns ratio=%.4f", capacity, retained, observed, reference, float64(observed)/float64(reference))
		if capacity == 8192 && retained != 128 {
			t.Fatalf("unexpected loss below capacity: %d", retained)
		}
		if capacity == 16 && (retained > 16 || observed >= reference/2) {
			t.Fatal("saturated process map did not demonstrate retention loss")
		}
	}
}

func TestCPUBatchSnapshotKernel(t *testing.T) {
	if os.Getenv("KNO_BPF_TEST") != "1" {
		t.Skip("set KNO_BPF_TEST=1 with BPF privileges")
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatal(err)
	}
	p, err := NewCPUProbe()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	for i := uint32(0); i < 8192; i++ {
		if err := p.objects.CpuRuntime.Put(cpuKey{CgroupID: 999, TGID: i + 1000000, PID: i + 1000000}, cpuValue{Runtime: uint64(i + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := p.Snapshot()
	if err != nil || len(snapshot.Processes) == 0 || snapshot.Totals == nil || snapshot.CapturedAt.IsZero() {
		t.Fatalf("batch snapshot: processes=%d totals=%d err=%v", len(snapshot.Processes), len(snapshot.Totals), err)
	}
}
