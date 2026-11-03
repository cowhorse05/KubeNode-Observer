package topology

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeHostTask(t *testing.T, proc, cgroups string, pid, parent int, executable, args, cgroup string) {
	t.Helper()
	base := filepath.Join(proc, fmt.Sprint(pid))
	if err := os.MkdirAll(base, 0755); err != nil {
		t.Fatal(err)
	}
	fields := []string{"S", fmt.Sprint(parent)}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, fmt.Sprint(pid*100))
	for name, content := range map[string]string{
		"stat":    fmt.Sprint(pid) + " (with (space)) " + strings.Join(fields, " "),
		"cmdline": strings.ReplaceAll(args, " ", "\x00") + "\x00",
		"cgroup":  "0::" + cgroup + "\n", "comm": filepath.Base(executable) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(executable, filepath.Join(base, "exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cgroups, cgroup), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestKataIdentityDoesNotUseGuestPIDOrDuplicateVM(t *testing.T) {
	root := t.TempDir()
	proc, cg := filepath.Join(root, "proc"), filepath.Join(root, "cgroup")
	id := strings.Repeat("a", 64)
	fakeHostTask(t, proc, cg, 100, 1, "/opt/kata/bin/containerd-shim-kata-v2", "shim -namespace k8s.io -id "+id, "/host/shim")
	fakeHostTask(t, proc, cg, 101, 100, "/opt/kata/bin/qemu-system-x86_64", "qemu", "/host/vmm")
	// Guest PID 123 happens to be a real, unrelated host PID. Reading its
	// cgroup would silently attach somebody else's CPU and processes.
	fakeHostTask(t, proc, cg, 123, 1, "/usr/bin/unrelated", "unrelated", "/wrong")
	runtime := fakeRuntime{containers: []RuntimeContainer{
		{ID: "one", SandboxID: id, RuntimeType: "io.containerd.kata.v2", RuntimeHandler: "kata-qemu", PID: 123},
		{ID: "two", SandboxID: id, RuntimeType: "io.containerd.kata.v2", RuntimeHandler: "kata-qemu", PID: 123},
	}, sandboxes: []Sandbox{{ID: id, RuntimeHandler: "kata-qemu"}}}
	snapshot, err := NewCollector("node", runtime, NewProcFS(proc, cg), nil).Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range snapshot.Containers {
		if c.PID != 0 || c.CgroupID != 0 || len(c.Processes) != 0 || c.ProcessVisibility != "guest-unavailable" {
			t.Fatalf("guest PID leaked: %+v", c)
		}
	}
	if len(snapshot.Sandboxes) != 1 || snapshot.Sandboxes[0].Shim == nil || snapshot.Sandboxes[0].Hypervisor == nil {
		t.Fatalf("missing sandbox: %+v", snapshot.Sandboxes)
	}
	s := snapshot.Sandboxes[0]
	if s.Shim.PID != 100 || s.Hypervisor.PID != 101 || s.Hypervisor.StartTimeTicks != 10100 || s.Hypervisor.CgroupID == 0 || len(s.Issues) != 0 {
		t.Fatalf("wrong host identity: %+v", s)
	}
	store := NewStore(nil)
	store.snapshot.Store(&snapshot)
	if _, _, ok := store.Resolve(s.Hypervisor.CgroupID); ok {
		t.Fatal("VMM incorrectly resolved as guest container")
	}
	if _, _, ok := store.Resolve(0); ok {
		t.Fatal("zero cgroup resolved")
	}
}

func TestKataRejectsUnrelatedAndAmbiguousProcesses(t *testing.T) {
	for _, scenario := range []string{"wrong-namespace", "wrong-sandbox", "not-descendant", "ambiguous-vmm", "ambiguous-shim"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			proc, cg := filepath.Join(root, "proc"), filepath.Join(root, "cg")
			args := "shim -namespace k8s.io -id a"
			if scenario == "wrong-namespace" {
				args = "shim -namespace other -id a"
			}
			if scenario == "wrong-sandbox" {
				args = "shim -namespace k8s.io -id b"
			}
			fakeHostTask(t, proc, cg, 10, 1, "/bin/containerd-shim-kata-v2", args, "/shim")
			parent := 10
			if scenario == "not-descendant" {
				parent = 1
			}
			fakeHostTask(t, proc, cg, 11, parent, "/bin/qemu-system-x86_64", "qemu", "/vmm")
			if scenario == "ambiguous-vmm" {
				fakeHostTask(t, proc, cg, 12, 10, "/bin/qemu-system-x86_64", "qemu", "/vmm")
			}
			if scenario == "ambiguous-shim" {
				fakeHostTask(t, proc, cg, 13, 1, "/bin/containerd-shim-kata-v2", args, "/shim2")
			}
			got := NewProcFS(proc, cg).KataSandboxes([]Sandbox{{ID: "a", RuntimeHandler: "kata-qemu"}})
			if len(got) != 1 || got[0].Hypervisor != nil || len(got[0].Issues) == 0 {
				t.Fatalf("accepted invalid identity: %+v", got)
			}
		})
	}
}

func TestKataHostIdentityRejectsPIDReuse(t *testing.T) {
	root := t.TempDir()
	proc, cg := filepath.Join(root, "proc"), filepath.Join(root, "cg")
	fakeHostTask(t, proc, cg, 10, 1, "/bin/qemu-system-x86_64", "qemu", "/vmm")
	p := NewProcFS(proc, cg)
	if _, err := p.hostIdentity(hostTask{pid: 10, start: 999}); err == nil {
		t.Fatal("PID reuse accepted")
	}
}
