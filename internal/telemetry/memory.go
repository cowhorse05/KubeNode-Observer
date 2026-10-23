package telemetry

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Memory values overlap: kernel/slab subtotals and map memlock must not be
// summed into memory.current. Max remains a string to represent "max" safely.
type Memory struct {
	Current uint64            `json:"currentBytes"`
	Peak    *uint64           `json:"peakBytes,omitempty"`
	Max     string            `json:"maxBytes"`
	Stat    map[string]uint64 `json:"stat"`
	Events  map[string]uint64 `json:"events"`
}

func readCounters(path string) (map[string]uint64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	result := map[string]uint64{}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid counter line in %s", path)
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		result[fields[0]] = value
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("empty counters in %s", path)
	}
	return result, nil
}

func readNumber(path string) (uint64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(content)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	return value, nil
}

func readMemory(root, path string) (*Memory, error) {
	// path is resolver-owned /proc/self/cgroup data, but keep the reader safe
	// independently of its caller as well.
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("absolute cgroup path required")
	}
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return nil, fmt.Errorf("invalid memory cgroup path")
		}
	}
	dir := filepath.Join(root, strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)))
	result := &Memory{}
	var err error
	result.Current, err = readNumber(filepath.Join(dir, "memory.current"))
	if err != nil {
		return nil, err
	}
	maximum, err := os.ReadFile(filepath.Join(dir, "memory.max"))
	if err != nil {
		return nil, err
	}
	result.Max = strings.TrimSpace(string(maximum))
	if result.Max != "max" {
		if _, err := strconv.ParseUint(result.Max, 10, 64); err != nil {
			return nil, fmt.Errorf("invalid memory.max: %w", err)
		}
	}
	peak, err := readNumber(filepath.Join(dir, "memory.peak"))
	if err == nil {
		result.Peak = &peak
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	result.Stat, err = readCounters(filepath.Join(dir, "memory.stat"))
	if err != nil {
		return nil, err
	}
	result.Events, err = readCounters(filepath.Join(dir, "memory.events"))
	if err != nil {
		return nil, err
	}
	for _, key := range []string{"anon", "file", "kernel"} {
		if _, ok := result.Stat[key]; !ok {
			return nil, fmt.Errorf("memory.stat missing %s", key)
		}
	}
	for _, key := range []string{"oom", "oom_kill"} {
		if _, ok := result.Events[key]; !ok {
			return nil, fmt.Errorf("memory.events missing %s", key)
		}
	}
	return result, nil
}

func readRSS(procRoot string) (uint64, error) {
	content, err := os.ReadFile(filepath.Join(procRoot, "self", "statm"))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(content))
	if len(fields) < 2 {
		return 0, fmt.Errorf("statm missing RSS")
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return pages * uint64(os.Getpagesize()), nil
}
