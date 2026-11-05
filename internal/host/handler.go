package host

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/runqlat"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type TopologySource interface {
	CurrentSnapshot() (*topology.Snapshot, error)
}
type CPUReader interface {
	Snapshot() (probe.CPUSnapshot, error)
}
type LatencyReader interface {
	Snapshot() ([]probe.RunQLatHistogram, error)
}
type ProcessReader interface {
	ReadVMM(topology.HostProcess) (ProcessView, error)
	ContainerIdentity(topology.Container) (topology.HostProcess, error)
	Validate(topology.HostProcess) error
	ContainerMemory(string) (*uint64, error)
}

type CPU struct {
	Accounting    string  `json:"accounting"`
	RuntimeNS     uint64  `json:"runtimeNs"`
	Percent       float64 `json:"percent"`
	WindowSeconds float64 `json:"windowSeconds"`
}
type Memory struct {
	Bytes  uint64 `json:"bytes"`
	Source string `json:"source"`
}
type Thread struct {
	TID            uint32           `json:"tid"`
	KernelTID      uint32           `json:"kernelTid,omitempty"`
	KernelTGID     uint32           `json:"kernelTgid,omitempty"`
	StartTimeTicks uint64           `json:"startTimeTicks"`
	CgroupID       uint64           `json:"cgroupId"`
	Command        string           `json:"command"`
	Role           string           `json:"role"`
	VCPUIndex      *uint32          `json:"vcpuIndex,omitempty"`
	CPU            *CPU             `json:"cpu"`
	RunQLat        *runqlat.Latency `json:"runqlat"`
	Issues         []string         `json:"issues,omitempty"`
}
type Item struct {
	Kind        string           `json:"kind"`
	Namespace   string           `json:"namespace"`
	PodName     string           `json:"podName"`
	PodUID      string           `json:"podUid"`
	SandboxID   string           `json:"sandboxId"`
	ContainerID string           `json:"containerId,omitempty"`
	PID         int              `json:"pid,omitempty"`
	CPU         *CPU             `json:"cpu"`
	Memory      *Memory          `json:"memory"`
	IO          *IO              `json:"io"`
	Threads     []Thread         `json:"threads"`
	VCPURunQLat *runqlat.Latency `json:"vcpuRunqlat"`
	Partial     bool             `json:"partial"`
	Issues      []string         `json:"issues,omitempty"`
}
type Response struct {
	Domain          string    `json:"domain"`
	RoleSource      string    `json:"roleSource"`
	StartedAt       time.Time `json:"startedAt"`
	DurationSeconds float64   `json:"durationSeconds"`
	TotalItems      int       `json:"totalItems"`
	Items           []Item    `json:"items"`
}
type Handler struct {
	topology TopologySource
	cpu      CPUReader
	latency  LatencyReader
	proc     ProcessReader
	mu       sync.Mutex
}

func NewHandler(t TopologySource, c CPUReader, l LatencyReader, p ProcessReader) *Handler {
	return &Handler{topology: t, cpu: c, latency: l, proc: p}
}

type selection struct {
	namespace, pod, sandbox, kind string
	duration                      time.Duration
	limit                         int
}
type target struct {
	item      Item
	container *topology.Container
	vmm       *topology.HostProcess
}
type processSample struct {
	identity topology.HostProcess
	view     ProcessView
	err      error
}

func fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}
func parseSelection(r *http.Request) (selection, error) {
	s := selection{kind: "all", duration: 5 * time.Second, limit: 20}
	for key, values := range r.URL.Query() {
		if len(values) != 1 {
			return s, fmt.Errorf("repeated query parameter: %s", key)
		}
		switch key {
		case "namespace":
			s.namespace = values[0]
		case "pod":
			s.pod = values[0]
		case "sandbox":
			s.sandbox = values[0]
		case "kind":
			s.kind = values[0]
		case "duration":
			value, err := time.ParseDuration(values[0])
			if err != nil || value < 100*time.Millisecond || value > 30*time.Second {
				return s, fmt.Errorf("duration must be 100ms..30s")
			}
			s.duration = value
		case "limit":
			value, err := strconv.Atoi(values[0])
			if err != nil || value < 1 || value > 100 {
				return s, fmt.Errorf("limit must be 1..100")
			}
			s.limit = value
		default:
			return s, fmt.Errorf("unknown query parameter: %s", key)
		}
	}
	if s.kind != "all" && s.kind != "kata" && s.kind != "runc" {
		return s, fmt.Errorf("kind must be all, kata or runc")
	}
	if s.sandbox != "" && (len(s.sandbox) != 64 || strings.Trim(s.sandbox, "0123456789abcdef") != "") {
		return s, fmt.Errorf("full lowercase hex sandbox ID required")
	}
	return s, nil
}
func targets(snapshot *topology.Snapshot, s selection) ([]target, error) {
	var result []target
	match := func(ns, pod, sandbox string) bool {
		return (s.namespace == "" || s.namespace == ns) && (s.pod == "" || s.pod == pod) && (s.sandbox == "" || s.sandbox == sandbox)
	}
	if s.kind != "runc" {
		for _, vm := range snapshot.Sandboxes {
			if !match(vm.Namespace, vm.PodName, vm.ID) {
				continue
			}
			result = append(result, target{item: Item{Kind: "kata-sandbox", Namespace: vm.Namespace, PodName: vm.PodName, PodUID: vm.PodUID, SandboxID: vm.ID, Threads: []Thread{}}, vmm: vm.Hypervisor})
		}
	}
	if s.kind != "kata" {
		for _, c := range snapshot.Containers {
			if c.ProcessVisibility != "host" || (c.RuntimeType != "" && !strings.HasPrefix(c.RuntimeType, "io.containerd.runc.")) || !match(c.Namespace, c.PodName, c.SandboxID) {
				continue
			}
			copy := c
			result = append(result, target{item: Item{Kind: "runc-container", Namespace: c.Namespace, PodName: c.PodName, PodUID: c.PodUID, SandboxID: c.SandboxID, ContainerID: c.ID, Threads: []Thread{}}, container: &copy})
		}
	}
	if len(result) > 256 {
		return nil, fmt.Errorf("Host selection exceeds 256 row bound")
	}
	return result, nil
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		fail(w, 405, "method_not_allowed", "only GET supported")
		return
	}
	s, err := parseSelection(r)
	if err != nil {
		fail(w, 400, "invalid_query", err.Error())
		return
	}
	if !h.mu.TryLock() {
		w.Header().Set("Retry-After", "1")
		fail(w, 429, "measurement_busy", "another Host resource window is active")
		return
	}
	defer h.mu.Unlock()
	response, err := h.measure(r.Context(), s)
	if err != nil {
		if r.Context().Err() == nil {
			fail(w, 503, "measurement_unavailable", err.Error())
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}
func (h *Handler) processSamples(selected []target) []processSample {
	result := make([]processSample, len(selected))
	for i, t := range selected {
		if t.container != nil {
			result[i].identity, result[i].err = h.proc.ContainerIdentity(*t.container)
		} else if t.vmm == nil {
			result[i].err = fmt.Errorf("verified Host VMM unavailable")
		} else {
			result[i].identity = *t.vmm
			result[i].view, result[i].err = h.proc.ReadVMM(*t.vmm)
		}
	}
	return result
}
func (h *Handler) measure(ctx context.Context, s selection) (Response, error) {
	response := Response{Domain: "host", RoleSource: "qemu-thread-name", StartedAt: time.Now().UTC(), Items: []Item{}}
	snapshot, err := h.topology.CurrentSnapshot()
	if err != nil {
		return response, err
	}
	selected, err := targets(snapshot, s)
	if err != nil {
		return response, err
	}
	if len(selected) == 0 {
		return response, nil
	}
	beforeProc := h.processSamples(selected)
	beforeCPU, err := h.cpu.Snapshot()
	if err != nil {
		return response, err
	}
	beforeLat, err := h.latency.Snapshot()
	if err != nil {
		return response, err
	}
	timer := time.NewTimer(s.duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return response, ctx.Err()
	case <-timer.C:
	}
	afterCPU, err := h.cpu.Snapshot()
	if err != nil {
		return response, err
	}
	afterLat, err := h.latency.Snapshot()
	if err != nil {
		return response, err
	}
	afterProc := h.processSamples(selected)
	current, err := h.topology.CurrentSnapshot()
	if err != nil {
		return response, err
	}
	live, err := targets(current, s)
	if err != nil {
		return response, err
	}
	for i, t := range selected {
		row := t.item
		if beforeProc[i].err != nil || afterProc[i].err != nil {
			row.Issues = append(row.Issues, fmt.Sprintf("Host identity/read unavailable: start=%v end=%v", beforeProc[i].err, afterProc[i].err))
		} else if !sameTarget(t, live) || beforeProc[i].identity != afterProc[i].identity {
			row.Issues = append(row.Issues, "Host target generation/cgroup or topology changed during window")
		} else if t.container != nil {
			row.CPU, err = cgroupCPU(t.container.CgroupID, beforeCPU, afterCPU)
			if err != nil {
				row.Issues = append(row.Issues, err.Error())
			}
			memory, e := h.proc.ContainerMemory(t.container.CgroupPath)
			if e != nil {
				row.Issues = append(row.Issues, "cgroup memory unavailable: "+e.Error())
			} else {
				row.Memory = &Memory{Bytes: *memory, Source: "cgroup-memory.current"}
			}
		} else {
			row = measureVMM(row, beforeProc[i].view, afterProc[i].view, beforeCPU, afterCPU, beforeLat, afterLat)
		}
		row.Partial = len(row.Issues) > 0
		for _, thread := range row.Threads {
			if len(thread.Issues) > 0 {
				row.Partial = true
			}
		}
		response.Items = append(response.Items, row)
	}
	sort.Slice(response.Items, func(i, j int) bool {
		a, b := response.Items[i], response.Items[j]
		if (a.CPU == nil) != (b.CPU == nil) {
			return a.CPU != nil
		}
		if a.CPU != nil && a.CPU.Percent != b.CPU.Percent {
			return a.CPU.Percent > b.CPU.Percent
		}
		return a.Kind+a.SandboxID+a.ContainerID < b.Kind+b.SandboxID+b.ContainerID
	})
	response.TotalItems = len(response.Items)
	if len(response.Items) > s.limit {
		response.Items = response.Items[:s.limit]
	}
	response.DurationSeconds = time.Since(response.StartedAt).Seconds()
	return response, nil
}
func sameTarget(t target, live []target) bool {
	for _, candidate := range live {
		if t.item.Kind != candidate.item.Kind || t.item.PodUID != candidate.item.PodUID || t.item.SandboxID != candidate.item.SandboxID {
			continue
		}
		if t.container != nil && candidate.container != nil {
			if t.container.ID != candidate.container.ID {
				continue
			}
			return t.container.ID == candidate.container.ID && t.container.PID == candidate.container.PID && t.container.CgroupID == candidate.container.CgroupID && t.container.CgroupPath == candidate.container.CgroupPath
		}
		if t.vmm != nil && candidate.vmm != nil {
			return *t.vmm == *candidate.vmm
		}
	}
	return false
}
