package probe

import (
	"os"
	"os/exec"
	"testing"

	"github.com/cilium/ebpf/rlimit"
)

func TestRunQLatTaskIdentityKernel(t *testing.T) {
	if os.Getenv("KNO_BPF_TEST") != "1" {
		t.Skip("set KNO_BPF_TEST=1 with BPF privileges")
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatal(err)
	}
	cpu, err := NewCPUProbe()
	if err != nil {
		t.Fatal(err)
	}
	defer cpu.Close()
	latency, err := NewRunQLatProbe()
	if err != nil {
		t.Fatal(err)
	}
	defer latency.Close()
	child := exec.Command("/bin/sleep", "0.2")
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	pid := uint32(child.Process.Pid)
	if err = child.Wait(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := cpu.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range snapshot.Processes {
		if task.NamespacePID == pid && task.NamespaceTGID == pid {
			identity, present, err := latency.TaskIdentity(task.PID)
			if err != nil || !present || identity.CgroupID != task.CgroupID || identity.TGID != task.TGID {
				t.Fatalf("scheduler identity differs: %+v %+v present=%v err=%v", task, identity, present, err)
			}
			t.Logf("namespace PID=%d kernel PID=%d TGID=%d cgroup=%d matched existing scheduler identity", pid, task.PID, identity.TGID, identity.CgroupID)
			return
		}
	}
	t.Fatal("child namespace alias missing")
}
