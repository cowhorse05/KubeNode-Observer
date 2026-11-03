package topology

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func isKata(runtimeType, handler string) bool {
	if runtimeType != "" {
		return strings.HasPrefix(runtimeType, "io.containerd.kata.") || strings.HasPrefix(runtimeType, "io.containerd.kata-")
	}
	return handler == "kata" || strings.HasPrefix(handler, "kata-")
}

type hostTask struct {
	pid, parent int
	start       uint64
	executable  string
	args        []string
}

// ParseTaskStat extracts identity fields without splitting a comm containing
// spaces/parentheses. start is field 22 in kernel clock ticks, not wall time.
func ParseTaskStat(content string) (parent int, start uint64, err error) {
	// comm can itself contain spaces and parentheses. Fields after its final
	// ')' start at state (3), so ppid (4) and starttime (22) are offsets 1/19.
	end := strings.LastIndex(content, ")")
	if end < 0 {
		return 0, 0, fmt.Errorf("invalid proc stat")
	}
	fields := strings.Fields(content[end+1:])
	if len(fields) < 20 {
		return 0, 0, fmt.Errorf("short proc stat")
	}
	parent, err = strconv.Atoi(fields[1])
	if err != nil {
		return
	}
	start, err = strconv.ParseUint(fields[19], 10, 64)
	return
}

func (p *ProcFS) hostTasks() (map[int]hostTask, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, err
	}
	tasks := make(map[int]hostTask)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		base := filepath.Join(p.root, entry.Name())
		stat, err := os.ReadFile(filepath.Join(base, "stat"))
		if err != nil {
			continue
		}
		parent, start, err := ParseTaskStat(string(stat))
		if err != nil {
			continue
		}
		executable, err := os.Readlink(filepath.Join(base, "exe"))
		if err != nil {
			continue
		}
		args, err := os.ReadFile(filepath.Join(base, "cmdline"))
		if err != nil {
			continue
		}
		tasks[pid] = hostTask{pid, parent, start, filepath.Base(executable), strings.Split(strings.TrimRight(string(args), "\x00"), "\x00")}
	}
	return tasks, nil
}

func argument(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimPrefix(arg, name+"=")
		}
	}
	return ""
}

func descendant(tasks map[int]hostTask, task hostTask, ancestor int) bool {
	for depth := 0; depth < 16; depth++ {
		if task.parent == ancestor {
			return true
		}
		parent, ok := tasks[task.parent]
		if !ok || parent.pid == task.pid {
			return false
		}
		task = parent
	}
	return false
}

func (p *ProcFS) hostIdentity(task hostTask) (*HostProcess, error) {
	path, err := p.CgroupPath(task.pid)
	if err != nil {
		return nil, err
	}
	id, err := p.CgroupID(path)
	if err != nil {
		return nil, err
	}
	stat, err := os.ReadFile(filepath.Join(p.root, strconv.Itoa(task.pid), "stat"))
	if err != nil {
		return nil, err
	}
	_, start, err := ParseTaskStat(string(stat))
	if err != nil || start != task.start {
		return nil, fmt.Errorf("host PID changed during discovery")
	}
	return &HostProcess{PID: task.pid, StartTimeTicks: start, Command: task.executable, CgroupPath: path, CgroupID: id}, nil
}

// KataSandboxes binds a CRI sandbox to an exact k8s.io shim -id, then to one
// allowlisted VMM descendant. Neither comm names nor arbitrary CRI PIDs suffice.
// Missing/ambiguous state is explicit; no guest process or per-container CPU is
// fabricated. No Kata internal state file schema is needed.
func (p *ProcFS) KataSandboxes(sandboxes []Sandbox) []Sandbox {
	result := make([]Sandbox, 0, len(sandboxes))
	if len(sandboxes) == 0 {
		return result
	}
	tasks, scanErr := p.hostTasks()
	for _, sandbox := range sandboxes {
		var shims []hostTask
		for _, task := range tasks {
			if task.executable == "containerd-shim-kata-v2" && argument(task.args, "-namespace") == "k8s.io" && argument(task.args, "-id") == sandbox.ID {
				shims = append(shims, task)
			}
		}
		if scanErr != nil {
			sandbox.Issues = append(sandbox.Issues, "scan host procfs: "+scanErr.Error())
		} else if len(shims) != 1 {
			sandbox.Issues = append(sandbox.Issues, fmt.Sprintf("expected one matching host Kata shim, found %d", len(shims)))
		} else {
			var err error
			sandbox.Shim, err = p.hostIdentity(shims[0])
			if err != nil {
				sandbox.Issues = append(sandbox.Issues, "shim identity: "+err.Error())
			}
			var vmms []hostTask
			for _, task := range tasks {
				if (task.executable == "qemu-system-x86_64" || task.executable == "cloud-hypervisor") && descendant(tasks, task, shims[0].pid) {
					vmms = append(vmms, task)
				}
			}
			if sandbox.Shim != nil && len(vmms) == 1 {
				sandbox.Hypervisor, err = p.hostIdentity(vmms[0])
				if err != nil {
					sandbox.Issues = append(sandbox.Issues, "hypervisor identity: "+err.Error())
				}
			} else {
				sandbox.Issues = append(sandbox.Issues, fmt.Sprintf("expected one VMM descendant of verified shim, found %d", len(vmms)))
			}
		}
		result = append(result, sandbox)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
