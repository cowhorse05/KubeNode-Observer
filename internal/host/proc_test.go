package host

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
func procStat(pid int, start uint64) string {
	fields := []string{"S", "1"}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, fmt.Sprint(start))
	return fmt.Sprintf("%d (with (spaces)) %s", pid, strings.Join(fields, " "))
}
func processFixture(t *testing.T) (*ProcReader, topology.HostProcess) {
	t.Helper()
	root := t.TempDir()
	proc, cg := filepath.Join(root, "proc"), filepath.Join(root, "cg")
	write(t, filepath.Join(proc, "100", "stat"), procStat(100, 50))
	write(t, filepath.Join(proc, "100", "cgroup"), "0::/vm\n")
	write(t, filepath.Join(proc, "100", "status"), "VmRSS:\t32 kB\n")
	write(t, filepath.Join(proc, "100", "io"), "read_bytes: 10\nwrite_bytes: 20\nrchar: 30\nwchar: 40\n")
	if err := os.Symlink("/opt/kata/bin/qemu-system-x86_64", filepath.Join(proc, "100", "exe")); err != nil {
		t.Fatal(err)
	}
	for _, tid := range []int{100, 101} {
		write(t, filepath.Join(proc, "100", "task", fmt.Sprint(tid), "stat"), procStat(tid, uint64(tid)))
		comm := "qemu-system-x86\n"
		if tid == 101 {
			comm = "CPU 0/KVM\n"
		}
		write(t, filepath.Join(proc, "100", "task", fmt.Sprint(tid), "comm"), comm)
		write(t, filepath.Join(proc, fmt.Sprint(tid), "cgroup"), "0::/vm\n")
	}
	if err := os.MkdirAll(filepath.Join(cg, "vm"), 0755); err != nil {
		t.Fatal(err)
	}
	reader := NewProcReader(proc, cg)
	id, err := reader.proc.CgroupID("/vm")
	if err != nil {
		t.Fatal(err)
	}
	return reader, topology.HostProcess{PID: 100, StartTimeTicks: 50, CgroupID: id, CgroupPath: "/vm", Command: "qemu-system-x86_64"}
}

func TestProcVMMIdentityRolesAndMeasurementUnits(t *testing.T) {
	p, id := processFixture(t)
	view, err := p.ReadVMM(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Threads) != 2 || view.Threads[100].Role != "vmm-main" || view.Threads[101].Role != "vcpu" || *view.Threads[101].VCPUIndex != 0 {
		t.Fatal(view.Threads)
	}
	if *view.RSSBytes != 32768 || view.IO.WriteBytes != 20 || len(view.Issues) != 0 {
		t.Fatal(view)
	}
	// A renamed thread is not still blindly labelled vCPU.
	write(t, filepath.Join(p.root, "100", "task", "101", "comm"), "worker\n")
	view, err = p.ReadVMM(id)
	if err != nil || view.Threads[101].Role != "other" {
		t.Fatal(view, err)
	}
}

func TestProcRejectsGenerationAndCgroupChanges(t *testing.T) {
	for _, scenario := range []string{"pid-reuse", "cgroup", "exe", "missing-io", "missing-rss"} {
		t.Run(scenario, func(t *testing.T) {
			p, id := processFixture(t)
			switch scenario {
			case "pid-reuse":
				write(t, filepath.Join(p.root, "100", "stat"), procStat(100, 51))
			case "cgroup":
				write(t, filepath.Join(p.root, "100", "cgroup"), "0::/other\n")
			case "exe":
				id.Command = "cloud-hypervisor"
			case "missing-io":
				write(t, filepath.Join(p.root, "100", "io"), "read_bytes: 1\n")
			case "missing-rss":
				write(t, filepath.Join(p.root, "100", "status"), "Name: qemu\n")
			}
			view, err := p.ReadVMM(id)
			if scenario == "missing-io" || scenario == "missing-rss" {
				if err != nil || len(view.Issues) == 0 {
					t.Fatal("partial proc counters not reported", view, err)
				}
				if scenario == "missing-io" && view.IO != nil {
					t.Fatal("missing IO became zero")
				}
				if scenario == "missing-rss" && view.RSSBytes != nil {
					t.Fatal("missing RSS became zero")
				}
			} else if err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}

func TestProcCountersRejectMalformedValuesAndReadCgroupMemory(t *testing.T) {
	for _, value := range []string{"VmRSS: 1 MB", "VmRSS: -1 kB", "VmRSS: 18446744073709551615 kB"} {
		if _, err := parseRSS(value); err == nil {
			t.Fatal(value)
		}
	}
	if _, err := parseIO("read_bytes: -1\nwrite_bytes: 2\nrchar: 3\nwchar: 4"); err == nil {
		t.Fatal("negative IO accepted")
	}
	p, _ := processFixture(t)
	write(t, filepath.Join(p.cgroupRoot, "vm", "memory.current"), "12345\n")
	value, err := p.ContainerMemory("/vm")
	if err != nil || *value != 12345 {
		t.Fatal(value, err)
	}
}
