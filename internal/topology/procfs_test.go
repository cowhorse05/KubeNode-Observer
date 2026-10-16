package topology

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcFSCgroupAndProcesses(t *testing.T) {
	root := t.TempDir()
	procRoot := filepath.Join(root, "proc")
	cgroupRoot := filepath.Join(root, "cgroup")
	cgroupPath := "/kubepods.slice/pod.scope/container.scope"

	for pid, command := range map[string]string{"100": "server\n", "101": "worker\n", "200": "other\n"} {
		processRoot := filepath.Join(procRoot, pid)
		if err := os.MkdirAll(processRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		path := cgroupPath
		if pid == "200" {
			path = "/system.slice/other.service"
		}
		if err := os.WriteFile(filepath.Join(processRoot, "cgroup"), []byte("0::"+path+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(processRoot, "comm"), []byte(command), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(cgroupRoot, cgroupPath), 0o755); err != nil {
		t.Fatal(err)
	}

	proc := NewProcFS(procRoot, cgroupRoot)
	path, err := proc.CgroupPath(100)
	if err != nil {
		t.Fatal(err)
	}
	if path != cgroupPath {
		t.Fatalf("CgroupPath() = %q, want %q", path, cgroupPath)
	}
	if id, err := proc.CgroupID(path); err != nil || id == 0 {
		t.Fatalf("CgroupID() = %d, %v", id, err)
	}
	processes, err := proc.Processes(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) != 2 || processes[0].PID != 100 || processes[1].Command != "worker" {
		t.Fatalf("Processes() = %#v", processes)
	}
}
