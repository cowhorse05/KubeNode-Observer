package guest

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/clockdomain"
	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/runqlat"
)

type CPUReader interface {
	Snapshot() (probe.CPUSnapshot, error)
}
type LatencyReader interface {
	Snapshot() ([]probe.RunQLatHistogram, error)
}
type IdentityReader interface {
	TaskIdentity(uint32) (probe.TaskIdentity, bool, error)
}
type Handler struct {
	Identity  Response
	CPU       CPUReader
	Latency   LatencyReader
	Proc      ProcSource
	Scheduler IdentityReader
	Clock     clockdomain.Source
	mu        sync.Mutex
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		writeError(w, 405, "method_not_allowed", "only GET supported")
		return
	}
	q, err := parse(r, false)
	if err != nil {
		writeError(w, 400, "invalid_query", err.Error())
		return
	}
	nonce := r.Header.Get(NonceHeader)
	if nonce == "" {
		nonce, err = NewID()
	}
	if err != nil || !hexID(nonce, 32) {
		writeError(w, 400, "invalid_query", "invalid request nonce")
		return
	}
	if !h.mu.TryLock() {
		w.Header().Set("Retry-After", "1")
		writeError(w, 429, "measurement_busy", "another Guest window is active")
		return
	}
	defer h.mu.Unlock()
	result, err := h.measure(r.Context(), q)
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, 503, "measurement_unavailable", err.Error())
		}
		return
	}
	result.RequestID = nonce
	writeResponse(w, result)
}
func (h *Handler) measure(ctx context.Context, q query) (Response, error) {
	result := h.Identity
	result.Domain = "guest"
	started := time.Now()
	result.StartedAt = started.UTC()
	result.Processes = []Process{}
	result.Timing = nil
	recorder, err := clockdomain.Begin(q.timing, h.Clock)
	if err != nil {
		return result, err
	}
	before, err := h.Proc.Snapshot(q.comm)
	if err != nil {
		return result, err
	}
	c0, err := clockdomain.Capture(recorder, clockdomain.CPUStart, h.CPU.Snapshot)
	if err != nil {
		return result, err
	}
	l0, err := clockdomain.Capture(recorder, clockdomain.RunQLatStart, h.Latency.Snapshot)
	if err != nil {
		return result, err
	}
	c0, err = scopeCPU(c0, before, h.Scheduler)
	if err != nil {
		return result, err
	}
	timer := time.NewTimer(q.duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return result, ctx.Err()
	case <-timer.C:
	}
	c1, err := clockdomain.Capture(recorder, clockdomain.CPUEnd, h.CPU.Snapshot)
	if err != nil {
		return result, err
	}
	l1, err := clockdomain.Capture(recorder, clockdomain.RunQLatEnd, h.Latency.Snapshot)
	if err != nil {
		return result, err
	}
	c1, err = scopeCPU(c1, before, h.Scheduler)
	if err != nil {
		return result, err
	}
	after, err := h.Proc.Snapshot(q.comm)
	if err != nil {
		return result, err
	}
	for pid, b := range before {
		a, ok := after[pid]
		row := Process{NamespacePID: pid, StartTimeTicks: b.Start, Command: b.Command, RunQLatCoverage: Coverage{TotalThreads: len(b.Threads)}}
		if !ok || a.Start != b.Start || a.Command != b.Command {
			row.Issues = append(row.Issues, "process disappeared/reused/renamed during window")
		} else {
			row = measureProcess(row, b, a, c0, c1, l0, l1)
		}
		row.Partial = len(row.Issues) > 0
		result.Processes = append(result.Processes, row)
	}
	sort.Slice(result.Processes, func(i, j int) bool {
		a, b := result.Processes[i], result.Processes[j]
		if (a.CPU == nil) != (b.CPU == nil) {
			return a.CPU != nil
		}
		if a.CPU != nil && a.CPU.Percent != b.CPU.Percent {
			return a.CPU.Percent > b.CPU.Percent
		}
		return a.NamespacePID < b.NamespacePID
	})
	result.TotalItems = len(result.Processes)
	if len(result.Processes) > q.limit {
		result.Processes = result.Processes[:q.limit]
	}
	result.DurationSeconds = time.Since(started).Seconds()
	result.Timing, err = recorder.Finish()
	if err != nil {
		return result, err
	}
	if result.Timing != nil && result.Timing.Identity.BootID != result.BootID {
		return result, fmt.Errorf("snapshot boot identity contradicts Probe identity")
	}
	return result, nil
}

// Kata exec can leave multiple historical cgroup keys for one namespace PID.
// Resolve against runqlat's existing last-switch-out identity; never choose the
// first/largest counter. The global PID/TGID and procfs generations still have
// to remain stable at both measurement endpoints.
func scopeCPU(snapshot probe.CPUSnapshot, views map[uint32]ProcView, source IdentityReader) (probe.CPUSnapshot, error) {
	if source == nil {
		return snapshot, fmt.Errorf("Guest scheduler identity source unavailable")
	}
	filtered := make([]probe.CPURuntime, 0)
	identities := make(map[uint32]probe.TaskIdentity)
	known := make(map[uint32]bool)
	looked := make(map[uint32]bool)
	for _, value := range snapshot.Processes {
		view, ok := views[value.NamespaceTGID]
		if !ok {
			continue
		}
		if _, ok = view.Threads[value.NamespacePID]; !ok {
			continue
		}
		if !looked[value.PID] {
			identity, present, err := source.TaskIdentity(value.PID)
			if err != nil {
				return snapshot, err
			}
			looked[value.PID] = true
			identities[value.PID] = identity
			known[value.PID] = present
		}
		identity := identities[value.PID]
		if known[value.PID] && identity.TGID == value.TGID && identity.CgroupID == value.CgroupID {
			filtered = append(filtered, value)
		}
	}
	snapshot.Processes = filtered
	return snapshot, nil
}
func alias(snapshot probe.CPUSnapshot, pid, tid uint32) (probe.CPURuntime, error) {
	var found probe.CPURuntime
	count := 0
	for _, v := range snapshot.Processes {
		if v.NamespaceTGID == pid && v.NamespacePID == tid {
			found = v
			count++
		}
	}
	if count != 1 {
		return found, fmt.Errorf("namespace CPU alias count=%d (missing or ambiguous cgroup)", count)
	}
	return found, nil
}
func histogram(values []probe.RunQLatHistogram, key probe.CPURuntime) (probe.RunQLatHistogram, bool) {
	for _, v := range values {
		if v.CgroupID == key.CgroupID && v.TGID == key.TGID && v.PID == key.PID {
			return v, true
		}
	}
	return probe.RunQLatHistogram{}, false
}
func measureProcess(row Process, b, a ProcView, c0, c1 probe.CPUSnapshot, l0, l1 []probe.RunQLatHistogram) Process {
	window := c1.DetailsCapturedAt.Sub(c0.DetailsCapturedAt)
	validCPU := len(b.Threads) > 0 && len(b.Threads) == len(a.Threads) && window > 0
	if len(b.Threads) != len(a.Threads) {
		row.Issues = append(row.Issues, fmt.Sprintf("thread set changed: before=%d after=%d", len(b.Threads), len(a.Threads)))
	}
	observed := 0
	row.RunQLatCoverage = Coverage{TotalThreads: len(b.Threads)}
	var runtime, wait, cgroup uint64
	var kernelPID uint32
	var buckets [64]uint64
	for tid, old := range b.Threads {
		current, ok := a.Threads[tid]
		if !ok || current != old {
			row.Issues = append(row.Issues, fmt.Sprintf("thread %d new/reused/renamed/disappeared", tid))
			validCPU = false
			continue
		}
		start, e0 := alias(c0, b.PID, tid)
		end, e1 := alias(c1, a.PID, tid)
		if e0 != nil || e1 != nil || start.PID != end.PID || start.TGID != end.TGID || start.CgroupID != end.CgroupID || end.Runtime < start.Runtime {
			row.Issues = append(row.Issues, fmt.Sprintf("thread %d CPU alias/key/counter unavailable or changed: start=%v end=%v", tid, e0, e1))
			validCPU = false
			continue
		}
		if kernelPID != 0 && (kernelPID != end.TGID || cgroup != end.CgroupID) {
			validCPU = false
			row.Issues = append(row.Issues, "process threads span multiple kernel TGIDs/cgroups")
		}
		kernelPID = end.TGID
		cgroup = end.CgroupID
		runtime += end.Runtime - start.Runtime
		before, ok0 := histogram(l0, start)
		after, ok1 := histogram(l1, end)
		if !ok0 || !ok1 || after.TotalNS < before.TotalNS || after.Count < before.Count {
			continue
		}
		validThread := true
		for i := range buckets {
			if after.Buckets[i] < before.Buckets[i] {
				validThread = false
				break
			}
		}
		if !validThread {
			continue
		}
		for i := range buckets {
			buckets[i] += after.Buckets[i] - before.Buckets[i]
		}
		wait += after.TotalNS - before.TotalNS
		observed++
	}
	if validCPU {
		row.PID = kernelPID
		row.CgroupID = cgroup
		row.CPU = &CPU{RuntimeNS: runtime, Percent: float64(runtime) / float64(window.Nanoseconds()) * 100, WindowSeconds: window.Seconds()}
	} else {
		row.Issues = append(row.Issues, "process CPU unavailable: incomplete stable thread set/counters")
	}
	if validCPU && observed > 0 {
		value := runqlat.SummarizeHistogram(wait, buckets)
		row.RunQLat = &value
		row.RunQLatCoverage.ObservedThreads = observed
		if observed < len(b.Threads) {
			row.Issues = append(row.Issues, fmt.Sprintf("runqlat partial coverage: %d/%d stable threads; missing/reset endpoints excluded, not zero-filled", observed, len(b.Threads)))
		}
	} else {
		row.Issues = append(row.Issues, "process runqlat unavailable: incomplete endpoints/thread set")
	}
	return row
}
