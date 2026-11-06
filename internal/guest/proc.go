package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type Task struct {
	TID     uint32
	Start   uint64
	Command string
}
type ProcView struct {
	PID     uint32
	Start   uint64
	Command string
	Threads map[uint32]Task
}
type ProcSource interface {
	Snapshot(string) (map[uint32]ProcView, error)
}
type ProcFS struct{ Root string }

// Snapshot sees the shared Pod PID namespace, not all Guest processes. Cgroup
// IDs come from BPF keys: a container cgroup namespace cannot stat siblings.
func (p ProcFS) Snapshot(comm string) (map[uint32]ProcView, error) {
	entries, err := os.ReadDir(p.Root)
	if err != nil {
		return nil, err
	}
	result := make(map[uint32]ProcView)
	visible := 0
	for _, entry := range entries {
		pid, e := strconv.ParseUint(entry.Name(), 10, 32)
		if e != nil || pid == 0 {
			continue
		}
		visible++
		if visible > 256 {
			return nil, fmt.Errorf("Guest procfs exceeds 256 process bound")
		}
		base := filepath.Join(p.Root, entry.Name())
		name, e := os.ReadFile(filepath.Join(base, "comm"))
		if e != nil {
			continue
		}
		command := strings.TrimSpace(string(name))
		if comm != "" && comm != command {
			continue
		}
		stat, e := os.ReadFile(filepath.Join(base, "stat"))
		if e != nil {
			continue
		}
		_, start, e := topology.ParseTaskStat(string(stat))
		if e != nil {
			return nil, e
		}
		view := ProcView{PID: uint32(pid), Start: start, Command: command, Threads: make(map[uint32]Task)}
		tasks, e := os.ReadDir(filepath.Join(base, "task"))
		if e != nil {
			return nil, e
		}
		if len(tasks) > 512 {
			return nil, fmt.Errorf("Guest process exceeds 512 thread bound")
		}
		for _, task := range tasks {
			tid, e := strconv.ParseUint(task.Name(), 10, 32)
			if e != nil {
				return nil, e
			}
			data, e := os.ReadFile(filepath.Join(base, "task", task.Name(), "stat"))
			if e != nil {
				return nil, fmt.Errorf("thread changed during procfs scan: %w", e)
			}
			_, tick, e := topology.ParseTaskStat(string(data))
			if e != nil {
				return nil, e
			}
			name, e := os.ReadFile(filepath.Join(base, "task", task.Name(), "comm"))
			if e != nil {
				return nil, e
			}
			view.Threads[uint32(tid)] = Task{TID: uint32(tid), Start: tick, Command: strings.TrimSpace(string(name))}
		}
		// Recheck generation after the thread scan; any incomplete scan fails.
		stat, e = os.ReadFile(filepath.Join(base, "stat"))
		if e != nil {
			return nil, e
		}
		_, end, e := topology.ParseTaskStat(string(stat))
		if e != nil || end != start {
			return nil, fmt.Errorf("Guest PID generation changed during scan")
		}
		result[uint32(pid)] = view
	}
	return result, nil
}
