package host

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

const maxThreads = 512

var vcpuName = regexp.MustCompile(`^CPU ([0-9]+)/KVM$`)

type IO struct {
	ReadBytes  uint64 `json:"readBytes"`
	WriteBytes uint64 `json:"writeBytes"`
	ReadChars  uint64 `json:"readChars"`
	WriteChars uint64 `json:"writeChars"`
}

type Task struct {
	TID            uint32
	StartTimeTicks uint64
	CgroupID       uint64
	Command        string
	Role           string
	VCPUIndex      *uint32
}

type ProcessView struct {
	Identity          topology.HostProcess
	At                time.Time
	Threads           map[uint32]Task
	IncompleteThreads bool
	RSSBytes          *uint64
	IO                *IO
	Issues            []string
}

type ProcReader struct {
	root, cgroupRoot string
	proc             *topology.ProcFS
}

func NewProcReader(root, cgroupRoot string) *ProcReader {
	return &ProcReader{root: root, cgroupRoot: cgroupRoot, proc: topology.NewProcFS(root, cgroupRoot)}
}

func (p *ProcReader) Validate(expected topology.HostProcess) error {
	if expected.PID <= 0 || expected.StartTimeTicks == 0 || expected.CgroupID == 0 {
		return fmt.Errorf("incomplete Host process identity")
	}
	base := filepath.Join(p.root, strconv.Itoa(expected.PID))
	stat, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return err
	}
	_, start, err := topology.ParseTaskStat(string(stat))
	if err != nil || start != expected.StartTimeTicks {
		return fmt.Errorf("Host PID generation changed")
	}
	exe, err := os.Readlink(filepath.Join(base, "exe"))
	if err != nil || filepath.Base(exe) != expected.Command {
		return fmt.Errorf("Host executable changed")
	}
	path, err := p.proc.CgroupPath(expected.PID)
	if err != nil || path != expected.CgroupPath {
		return fmt.Errorf("Host process cgroup changed")
	}
	id, err := p.proc.CgroupID(path)
	if err != nil || id != expected.CgroupID {
		return fmt.Errorf("Host process cgroup inode changed")
	}
	return nil
}

func (p *ProcReader) ReadVMM(expected topology.HostProcess) (ProcessView, error) {
	result := ProcessView{Identity: expected, At: time.Now(), Threads: make(map[uint32]Task)}
	if err := p.Validate(expected); err != nil {
		return result, err
	}
	if expected.Command != "qemu-system-x86_64" {
		return result, fmt.Errorf("Host thread roles only verified for QEMU/KVM")
	}
	base := filepath.Join(p.root, strconv.Itoa(expected.PID))
	entries, err := os.ReadDir(filepath.Join(base, "task"))
	if err != nil {
		return result, err
	}
	if len(entries) > maxThreads {
		return result, fmt.Errorf("VMM exceeds %d thread bound", maxThreads)
	}
	for _, entry := range entries {
		tid, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || tid == 0 {
			continue
		}
		taskBase := filepath.Join(base, "task", entry.Name())
		stat, err := os.ReadFile(filepath.Join(taskBase, "stat"))
		if err != nil {
			result.Issues = append(result.Issues, "thread disappeared during procfs scan")
			result.IncompleteThreads = true
			continue
		}
		_, start, err := topology.ParseTaskStat(string(stat))
		if err != nil {
			return result, err
		}
		comm, err := os.ReadFile(filepath.Join(taskBase, "comm"))
		if err != nil {
			return result, err
		}
		path, err := p.proc.CgroupPath(int(tid))
		if err != nil {
			return result, err
		}
		id, err := p.proc.CgroupID(path)
		if err != nil {
			return result, err
		}
		task := Task{TID: uint32(tid), StartTimeTicks: start, CgroupID: id, Command: strings.TrimSpace(string(comm)), Role: "other"}
		if int(tid) == expected.PID {
			task.Role = "vmm-main"
		}
		if match := vcpuName.FindStringSubmatch(task.Command); match != nil {
			index, err := strconv.ParseUint(match[1], 10, 32)
			if err == nil {
				i := uint32(index)
				task.VCPUIndex = &i
				task.Role = "vcpu"
			}
		}
		result.Threads[task.TID] = task
	}
	if len(result.Threads) == 0 {
		return result, fmt.Errorf("VMM has no readable Host threads")
	}
	status, err := os.ReadFile(filepath.Join(base, "status"))
	if err == nil {
		result.RSSBytes, err = parseRSS(string(status))
	}
	if err != nil {
		result.Issues = append(result.Issues, "RSS unavailable: "+err.Error())
	}
	io, err := os.ReadFile(filepath.Join(base, "io"))
	if err == nil {
		result.IO, err = parseIO(string(io))
	}
	if err != nil {
		result.Issues = append(result.Issues, "IO unavailable: "+err.Error())
	}
	if err := p.Validate(expected); err != nil {
		return result, err
	}
	return result, nil
}

func (p *ProcReader) ContainerIdentity(c topology.Container) (topology.HostProcess, error) {
	identity := topology.HostProcess{PID: c.PID, CgroupID: c.CgroupID, CgroupPath: c.CgroupPath}
	base := filepath.Join(p.root, strconv.Itoa(c.PID))
	data, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return identity, err
	}
	_, identity.StartTimeTicks, err = topology.ParseTaskStat(string(data))
	if err != nil {
		return identity, err
	}
	exe, err := os.Readlink(filepath.Join(base, "exe"))
	if err != nil {
		return identity, err
	}
	identity.Command = filepath.Base(exe)
	return identity, p.Validate(identity)
}

func parseRSS(content string) (*uint64, error) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == "VmRSS:" {
			if len(fields) != 3 || fields[2] != "kB" {
				return nil, fmt.Errorf("invalid VmRSS units")
			}
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || value > math.MaxUint64/1024 {
				return nil, fmt.Errorf("invalid VmRSS")
			}
			value *= 1024
			return &value, nil
		}
	}
	return nil, fmt.Errorf("VmRSS missing")
}

func parseIO(content string) (*IO, error) {
	values := make(map[string]uint64)
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "read_bytes:", "write_bytes:", "rchar:", "wchar:":
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return nil, err
			}
			values[fields[0]] = value
		}
	}
	if len(values) != 4 {
		return nil, fmt.Errorf("incomplete procfs IO")
	}
	return &IO{values["read_bytes:"], values["write_bytes:"], values["rchar:"], values["wchar:"]}, nil
}

func (p *ProcReader) ContainerMemory(path string) (*uint64, error) {
	path = filepath.Join(p.cgroupRoot, strings.TrimPrefix(filepath.Clean("/"+path), "/"), "memory.current")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return nil, err
	}
	return &value, nil
}
