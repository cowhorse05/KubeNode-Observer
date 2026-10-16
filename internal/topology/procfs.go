package topology

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type ProcFS struct {
	root       string
	cgroupRoot string
}

func NewProcFS(root, cgroupRoot string) *ProcFS {
	return &ProcFS{root: root, cgroupRoot: cgroupRoot}
}

func (p *ProcFS) CgroupPath(pid int) (string, error) {
	file, err := os.Open(filepath.Join(p.root, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 3)
		if len(parts) == 3 && parts[0] == "0" && parts[1] == "" {
			return filepath.Clean(parts[2]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("cgroup v2 entry not found for pid %d", pid)
}

func (p *ProcFS) CgroupID(path string) (uint64, error) {
	cleanPath := filepath.Clean("/" + path)
	fullPath := filepath.Join(p.cgroupRoot, strings.TrimPrefix(cleanPath, "/"))
	info, err := os.Stat(fullPath)
	if err != nil {
		return 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unsupported stat result for %s", fullPath)
	}
	return stat.Ino, nil
}

func (p *ProcFS) Processes(cgroupPath string) ([]Process, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		return nil, err
	}

	processes := make([]Process, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		path, err := p.CgroupPath(pid)
		if err != nil || (path != cgroupPath && !strings.HasPrefix(path, strings.TrimSuffix(cgroupPath, "/")+"/")) {
			continue
		}
		command, err := os.ReadFile(filepath.Join(p.root, entry.Name(), "comm"))
		if err != nil {
			continue
		}
		processes = append(processes, Process{PID: pid, Command: strings.TrimSpace(string(command))})
	}

	sort.Slice(processes, func(i, j int) bool { return processes[i].PID < processes[j].PID })
	return processes, nil
}
