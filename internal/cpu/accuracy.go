package cpu

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// AccuracyHandler is an explicit diagnostic experiment, not a background metric.
// It samples the same runtime map and delta rule as Collector, at a requested
// interval. It does not integrate /api/v1/cpu's published one-second windows.
type AccuracyHandler struct {
	source   RuntimeSource
	resolver Resolver
	root     string
	mu       sync.Mutex
}

type AccuracySample struct {
	At                time.Time `json:"at"`
	ReadSeconds       float64   `json:"readSeconds"`
	ReferenceBeforeUS uint64    `json:"referenceBeforeUs"`
	ReferenceAfterUS  uint64    `json:"referenceAfterUs"`
	ThrottledUS       uint64    `json:"throttledUs"`
	Entries           int       `json:"entries"`
	MapEntries        int       `json:"mapEntries"`
	ObservedRuntimeNS uint64    `json:"observedRuntimeNs"`
	NewKeys           int       `json:"newKeys"`
	MissingKeys       int       `json:"missingKeys"`
	ResetKeys         int       `json:"resetKeys"`
}

type AccuracyResponse struct {
	CgroupID             uint64           `json:"cgroupId"`
	ContainerID          string           `json:"containerId"`
	CgroupPath           string           `json:"cgroupPath"`
	IntervalSeconds      float64          `json:"intervalSeconds"`
	WindowSeconds        float64          `json:"windowSeconds"`
	ObservedRuntimeNS    uint64           `json:"observedRuntimeNs"`
	ReferenceLowerNS     uint64           `json:"referenceLowerNs"`
	ReferenceUpperNS     uint64           `json:"referenceUpperNs"`
	SignedErrorPercent   float64          `json:"signedErrorPercent"`
	AbsoluteErrorPercent float64          `json:"absoluteErrorPercent"`
	ReferenceSkewPercent float64          `json:"referenceSkewPercent"`
	ThrottledUS          uint64           `json:"throttledUs"`
	Samples              []AccuracySample `json:"samples"`
}

func NewAccuracyHandler(source RuntimeSource, resolver Resolver, root string) *AccuracyHandler {
	return &AccuracyHandler{source: source, resolver: resolver, root: root}
}

func (h *AccuracyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	id, err := strconv.ParseUint(r.URL.Query().Get("cgroupId"), 10, 64)
	if err != nil || id == 0 {
		http.Error(w, "positive cgroupId required", 400)
		return
	}
	duration, err := time.ParseDuration(r.URL.Query().Get("duration"))
	if err != nil || duration < time.Second || duration > time.Minute {
		http.Error(w, "duration must be between 1s and 1m", 400)
		return
	}
	interval, err := time.ParseDuration(r.URL.Query().Get("interval"))
	if err != nil || interval < 100*time.Millisecond || interval > duration {
		http.Error(w, "interval must be between 100ms and duration", 400)
		return
	}
	container, source, ok := h.resolver.Resolve(id)
	if !ok || source != "current" || container.CgroupPath == "" {
		http.Error(w, "current container cgroup not found", 404)
		return
	}
	if !h.mu.TryLock() {
		http.Error(w, "accuracy experiment already running", 429)
		return
	}
	defer h.mu.Unlock()
	// Only a resolver-owned path is used. Do not accept arbitrary host paths.
	path := filepath.Join(h.root, strings.TrimPrefix(filepath.Clean(container.CgroupPath), string(filepath.Separator)))
	relative, err := filepath.Rel(h.root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		http.Error(w, "invalid cgroup path", 400)
		return
	}
	response, err := measureAccuracy(r.Context(), h.source, path, id, duration, interval)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	response.ContainerID = container.ID
	response.CgroupPath = container.CgroupPath
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func readCPUStat(path string, id uint64) (usageUS, throttledUS uint64, err error) {
	var stat unix.Stat_t
	if err = unix.Stat(path, &stat); err != nil {
		return
	}
	if stat.Ino != id {
		return 0, 0, fmt.Errorf("cgroup inode changed: got %d, want %d", stat.Ino, id)
	}
	content, err := os.ReadFile(filepath.Join(path, "cpu.stat"))
	if err != nil {
		return 0, 0, err
	}
	found := false
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || (fields[0] != "usage_usec" && fields[0] != "throttled_usec") {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, 0, parseErr
		}
		if fields[0] == "usage_usec" {
			usageUS, found = value, true
		} else {
			throttledUS = value
		}
	}
	if !found {
		return 0, 0, fmt.Errorf("cpu.stat has no usage_usec")
	}
	return usageUS, throttledUS, nil
}

func measureAccuracy(ctx context.Context, source RuntimeSource, path string, id uint64, duration, interval time.Duration) (AccuracyResponse, error) {
	response := AccuracyResponse{CgroupID: id, IntervalSeconds: interval.Seconds()}
	previous := map[runtimeKey]uint64{}
	started := time.Now()
	deadline := started.Add(duration)
	for {
		if err := ctx.Err(); err != nil {
			return response, err
		}
		readStart := time.Now()
		before, _, err := readCPUStat(path, id)
		if err != nil {
			return response, err
		}
		values, err := source.Snapshot()
		if err != nil {
			return response, err
		}
		after, throttled, err := readCPUStat(path, id)
		if err != nil {
			return response, err
		}
		if after < before {
			return response, fmt.Errorf("cgroup counter regressed during map read")
		}
		sample := AccuracySample{At: readStart, ReadSeconds: time.Since(readStart).Seconds(), ReferenceBeforeUS: before, ReferenceAfterUS: after, ThrottledUS: throttled, MapEntries: len(values)}
		current := make(map[runtimeKey]uint64)
		for _, value := range values {
			if value.CgroupID != id {
				continue
			}
			key := runtimeKey{value.CgroupID, value.TGID, value.PID}
			current[key] = value.Runtime
		}
		sample.Entries = len(current)
		if len(response.Samples) > 0 {
			last := response.Samples[len(response.Samples)-1]
			if before < last.ReferenceAfterUS || after < before || throttled < last.ThrottledUS {
				return response, fmt.Errorf("cgroup counter regressed")
			}
			applyAccuracyDelta(&sample, current, previous)
			response.ObservedRuntimeNS += sample.ObservedRuntimeNS
		}
		response.Samples = append(response.Samples, sample)
		previous = current
		if !time.Now().Before(deadline) && len(response.Samples) >= 2 {
			break
		}
		// Spacing is measured between scan starts; slow scans are visible in raw data.
		next := readStart.Add(interval)
		if next.After(deadline) {
			next = deadline
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return response, ctx.Err()
		case <-timer.C:
		}
	}
	return summarizeAccuracy(response)
}

func applyAccuracyDelta(sample *AccuracySample, current, previous map[runtimeKey]uint64) {
	for key, value := range current {
		old, exists := previous[key]
		sample.ObservedRuntimeNS += runtimeDelta(value, old, exists)
		if !exists {
			sample.NewKeys++
		} else if value < old {
			sample.ResetKeys++
		}
	}
	for key := range previous {
		if _, exists := current[key]; !exists {
			sample.MissingKeys++
		}
	}
}

func summarizeAccuracy(response AccuracyResponse) (AccuracyResponse, error) {
	if len(response.Samples) < 2 {
		return response, fmt.Errorf("at least two snapshots required")
	}
	first, last := response.Samples[0], response.Samples[len(response.Samples)-1]
	if last.ReferenceBeforeUS < first.ReferenceAfterUS || last.ReferenceAfterUS < last.ReferenceBeforeUS {
		return response, fmt.Errorf("invalid reference window")
	}
	response.WindowSeconds = last.At.Sub(first.At).Seconds()
	response.ReferenceLowerNS = (last.ReferenceBeforeUS - first.ReferenceAfterUS) * 1000
	response.ReferenceUpperNS = (last.ReferenceAfterUS - first.ReferenceBeforeUS) * 1000
	response.ThrottledUS = last.ThrottledUS - first.ThrottledUS
	reference := (float64(response.ReferenceLowerNS) + float64(response.ReferenceUpperNS)) / 2
	if reference == 0 {
		return response, fmt.Errorf("zero reference CPU usage; no accuracy conclusion possible")
	}
	response.SignedErrorPercent = (float64(response.ObservedRuntimeNS)/reference - 1) * 100
	response.AbsoluteErrorPercent = math.Abs(response.SignedErrorPercent)
	response.ReferenceSkewPercent = float64(response.ReferenceUpperNS-response.ReferenceLowerNS) / reference * 100
	return response, nil
}
