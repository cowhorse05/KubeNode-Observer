package telemetry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

type Snapshot struct {
	At            time.Time         `json:"at"`
	Stages        map[string]Stage  `json:"stages"`
	TotalAlloc    uint64            `json:"totalAllocBytes"`
	HeapAlloc     uint64            `json:"heapAllocBytes"`
	HeapInuse     uint64            `json:"heapInuseBytes"`
	HeapIdle      uint64            `json:"heapIdleBytes"`
	HeapReleased  uint64            `json:"heapReleasedBytes"`
	StackInuse    uint64            `json:"stackInuseBytes"`
	GoSys         uint64            `json:"goSysBytes"`
	GoManaged     uint64            `json:"goManagedBytes"`
	GoMemoryLimit int64             `json:"goMemoryLimitBytes"`
	GCCPUFraction float64           `json:"gcCpuFraction"`
	RSS           uint64            `json:"rssBytes"`
	RSSError      string            `json:"rssError,omitempty"`
	Memory        *Memory           `json:"memory,omitempty"`
	MemoryError   string            `json:"memoryError,omitempty"`
	NumGC         uint32            `json:"numGc"`
	CgroupPath    string            `json:"cgroupPath"`
	CPUMax        string            `json:"cpuMax"`
	CPUStat       map[string]uint64 `json:"cpuStat"`
	Error         string            `json:"error,omitempty"`
}

func Handler(recorder *Recorder, procRoot, cgroupRoot string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		result := Snapshot{At: time.Now().UTC(), Stages: recorder.Snapshot(), TotalAlloc: memory.TotalAlloc, HeapAlloc: memory.HeapAlloc, NumGC: memory.NumGC}
		result.HeapInuse, result.HeapIdle, result.HeapReleased, result.StackInuse = memory.HeapInuse, memory.HeapIdle, memory.HeapReleased, memory.StackInuse
		result.GoSys, result.GoManaged, result.GoMemoryLimit, result.GCCPUFraction = memory.Sys, memory.Sys-memory.HeapReleased, debug.SetMemoryLimit(-1), memory.GCCPUFraction
		if rss, err := readRSS(procRoot); err != nil {
			result.RSSError = err.Error()
		} else {
			result.RSS = rss
		}
		path, maximum, stat, err := readSelfCgroup(procRoot, cgroupRoot)
		result.CgroupPath, result.CPUMax, result.CPUStat = path, maximum, stat
		if err != nil {
			result.Error = err.Error()
		} else if usage, err := readMemory(cgroupRoot, path); err != nil {
			result.MemoryError = err.Error()
		} else {
			result.Memory = usage
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}

// Resolve our actual v2 cgroup from procfs; /sys/fs/cgroup may expose the node
// rather than the container root, especially with hostPID enabled.
func readSelfCgroup(procRoot, root string) (string, string, map[string]uint64, error) {
	content, err := os.ReadFile(filepath.Join(procRoot, "self", "cgroup"))
	if err != nil {
		return "", "", nil, err
	}
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		path := strings.TrimPrefix(line, "0::")
		for _, part := range strings.Split(path, "/") {
			if part == ".." {
				return "", "", nil, fmt.Errorf("invalid self cgroup path")
			}
		}
		dir := filepath.Join(root, strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)))
		maximum, err := os.ReadFile(filepath.Join(dir, "cpu.max"))
		if err != nil {
			return path, "", nil, err
		}
		stat, err := os.ReadFile(filepath.Join(dir, "cpu.stat"))
		if err != nil {
			return path, "", nil, err
		}
		values := make(map[string]uint64)
		for _, line := range strings.Split(string(stat), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return path, "", nil, err
			}
			values[fields[0]] = value
		}
		if _, ok := values["usage_usec"]; !ok {
			return path, "", nil, fmt.Errorf("cpu.stat missing usage_usec")
		}
		return path, strings.TrimSpace(string(maximum)), values, nil
	}
	return "", "", nil, fmt.Errorf("self cgroup v2 path not found")
}
