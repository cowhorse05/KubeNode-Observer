package cpu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type RuntimeSource interface {
	Snapshot() ([]probe.CPURuntime, error)
}

type Resolver interface {
	Resolve(cgroupID uint64) (topology.Container, string, bool)
}

type ProcessUsage struct {
	Namespace        string  `json:"namespace"`
	PodName          string  `json:"podName"`
	PodUID           string  `json:"podUid"`
	ContainerName    string  `json:"containerName"`
	ContainerID      string  `json:"containerId"`
	CgroupID         uint64  `json:"cgroupId"`
	PID              uint32  `json:"pid"`
	TGID             uint32  `json:"tgid"`
	Command          string  `json:"command"`
	RuntimeNS        uint64  `json:"runtimeNs"`
	CPUPercent       float64 `json:"cpuPercent"`
	MetadataSource   string  `json:"metadataSource"`
	KubernetesPodUID string  `json:"kubernetesPodUid,omitempty"`
}

type Snapshot struct {
	SampledAt       time.Time      `json:"sampledAt"`
	WindowSeconds   float64        `json:"windowSeconds"`
	Processes       []ProcessUsage `json:"processes"`
	UnresolvedTasks int            `json:"unresolvedTasks"`
}

type runtimeKey struct {
	cgroupID uint64
	tgid     uint32
	pid      uint32
}

type processKey struct {
	cgroupID uint64
	tgid     uint32
}

type Collector struct {
	source   RuntimeSource
	resolver Resolver

	collectMu sync.Mutex
	previous  map[runtimeKey]uint64
	lastAt    time.Time
	snapshot  atomic.Pointer[Snapshot]
	errorMu   sync.RWMutex
	lastError string
}

func NewCollector(source RuntimeSource, resolver Resolver) *Collector {
	return &Collector{source: source, resolver: resolver, previous: make(map[runtimeKey]uint64)}
}

func (c *Collector) Run(ctx context.Context, interval time.Duration) {
	_ = c.Collect(time.Now())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = c.Collect(time.Now())
		}
	}
}

func (c *Collector) Collect(now time.Time) error {
	c.collectMu.Lock()
	defer c.collectMu.Unlock()

	runtimes, err := c.source.Snapshot()
	if err != nil {
		c.setError(err)
		return err
	}

	current := make(map[runtimeKey]uint64, len(runtimes))
	if c.lastAt.IsZero() {
		for _, runtime := range runtimes {
			current[runtimeKey{runtime.CgroupID, runtime.TGID, runtime.PID}] = runtime.Runtime
		}
		c.previous = current
		c.lastAt = now
		c.snapshot.Store(&Snapshot{SampledAt: now, Processes: []ProcessUsage{}})
		c.setError(nil)
		return nil
	}

	window := now.Sub(c.lastAt)
	if window <= 0 {
		return fmt.Errorf("CPU collection time must move forward")
	}

	resolved := make(map[uint64]resolvedContainer)
	processes := make(map[processKey]ProcessUsage)
	unresolved := 0
	for _, runtime := range runtimes {
		key := runtimeKey{runtime.CgroupID, runtime.TGID, runtime.PID}
		current[key] = runtime.Runtime
		previous, existed := c.previous[key]
		delta := runtimeDelta(runtime.Runtime, previous, existed)
		if delta == 0 {
			continue
		}

		identity, cached := resolved[runtime.CgroupID]
		if !cached {
			container, source, ok := c.resolver.Resolve(runtime.CgroupID)
			identity = resolvedContainer{container: container, source: source, ok: ok}
			resolved[runtime.CgroupID] = identity
		}
		if !identity.ok {
			unresolved++
			continue
		}

		processID := processKey{cgroupID: runtime.CgroupID, tgid: runtime.TGID}
		usage := processes[processID]
		if usage.RuntimeNS == 0 || runtime.PID == runtime.TGID {
			usage.Command = runtime.Command()
		}
		usage.Namespace = identity.container.Namespace
		usage.PodName = identity.container.PodName
		usage.PodUID = identity.container.PodUID
		usage.KubernetesPodUID = identity.container.KubernetesPodUID
		usage.ContainerName = identity.container.Name
		usage.ContainerID = identity.container.ID
		usage.CgroupID = runtime.CgroupID
		usage.PID = runtime.TGID
		usage.TGID = runtime.TGID
		usage.RuntimeNS += delta
		usage.MetadataSource = identity.source
		processes[processID] = usage
	}

	result := make([]ProcessUsage, 0, len(processes))
	for _, usage := range processes {
		usage.CPUPercent = float64(usage.RuntimeNS) / float64(window.Nanoseconds()) * 100
		result = append(result, usage)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CPUPercent == result[j].CPUPercent {
			return result[i].PID < result[j].PID
		}
		return result[i].CPUPercent > result[j].CPUPercent
	})

	c.previous = current
	c.lastAt = now
	c.snapshot.Store(&Snapshot{
		SampledAt:       now,
		WindowSeconds:   window.Seconds(),
		Processes:       result,
		UnresolvedTasks: unresolved,
	})
	c.setError(nil)
	return nil
}

// runtimeDelta is shared by the production collector and the accuracy experiment.
// New or reset keys are treated as cumulative values since their creation.
func runtimeDelta(current, previous uint64, existed bool) uint64 {
	if existed && current >= previous {
		return current - previous
	}
	return current
}

type resolvedContainer struct {
	container topology.Container
	source    string
	ok        bool
}

func (c *Collector) setError(err error) {
	c.errorMu.Lock()
	defer c.errorMu.Unlock()
	if err == nil {
		c.lastError = ""
	} else {
		c.lastError = err.Error()
	}
}

type Usage struct {
	Scope         string  `json:"scope"`
	Namespace     string  `json:"namespace"`
	PodName       string  `json:"podName"`
	PodUID        string  `json:"podUid"`
	ContainerName string  `json:"containerName,omitempty"`
	ContainerID   string  `json:"containerId,omitempty"`
	CgroupID      uint64  `json:"cgroupId,omitempty"`
	PID           uint32  `json:"pid,omitempty"`
	TGID          uint32  `json:"tgid,omitempty"`
	Command       string  `json:"command,omitempty"`
	RuntimeNS     uint64  `json:"runtimeNs"`
	CPUPercent    float64 `json:"cpuPercent"`
}

type Response struct {
	SampledAt       time.Time `json:"sampledAt"`
	WindowSeconds   float64   `json:"windowSeconds"`
	Level           string    `json:"level"`
	Items           []Usage   `json:"items"`
	UnresolvedTasks int       `json:"unresolvedTasks"`
	Error           string    `json:"error,omitempty"`
}

func (c *Collector) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	level := request.URL.Query().Get("level")
	if level == "" {
		level = "pod"
	}
	if level != "pod" && level != "container" && level != "process" {
		http.Error(writer, "level must be pod, container, or process", http.StatusBadRequest)
		return
	}
	limit := 20
	if value := request.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 1000 {
			http.Error(writer, "limit must be between 1 and 1000", http.StatusBadRequest)
			return
		}
		limit = parsed
	}

	snapshot := c.snapshot.Load()
	if snapshot == nil || snapshot.WindowSeconds == 0 {
		http.Error(writer, "CPU sample is not ready", http.StatusServiceUnavailable)
		return
	}
	items := aggregate(snapshot.Processes, level, request.URL.Query().Get("namespace"), request.URL.Query().Get("pod"))
	if len(items) > limit {
		items = items[:limit]
	}
	c.errorMu.RLock()
	lastError := c.lastError
	c.errorMu.RUnlock()
	response := Response{
		SampledAt:       snapshot.SampledAt,
		WindowSeconds:   snapshot.WindowSeconds,
		Level:           level,
		Items:           items,
		UnresolvedTasks: snapshot.UnresolvedTasks,
		Error:           lastError,
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(response)
}

func aggregate(processes []ProcessUsage, level, namespace, pod string) []Usage {
	grouped := make(map[string]Usage)
	for _, process := range processes {
		if namespace != "" && process.Namespace != namespace {
			continue
		}
		if pod != "" && process.PodName != pod {
			continue
		}
		item := Usage{
			Scope:         level,
			Namespace:     process.Namespace,
			PodName:       process.PodName,
			PodUID:        process.PodUID,
			ContainerName: process.ContainerName,
			ContainerID:   process.ContainerID,
			CgroupID:      process.CgroupID,
			PID:           process.PID,
			TGID:          process.TGID,
			Command:       process.Command,
			RuntimeNS:     process.RuntimeNS,
			CPUPercent:    process.CPUPercent,
		}
		key := fmt.Sprintf("%d/%d", process.CgroupID, process.TGID)
		switch level {
		case "pod":
			key = process.Namespace + "/" + process.PodUID
			item.ContainerName = ""
			item.ContainerID = ""
			item.CgroupID = 0
			item.PID = 0
			item.TGID = 0
			item.Command = ""
		case "container":
			key = process.ContainerID
			item.PID = 0
			item.TGID = 0
			item.Command = ""
		}
		if existing, ok := grouped[key]; ok {
			existing.RuntimeNS += item.RuntimeNS
			existing.CPUPercent += item.CPUPercent
			grouped[key] = existing
		} else {
			grouped[key] = item
		}
	}

	items := make([]Usage, 0, len(grouped))
	for _, item := range grouped {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CPUPercent == items[j].CPUPercent {
			return items[i].Namespace+items[i].PodName+items[i].ContainerName+items[i].Command < items[j].Namespace+items[j].PodName+items[j].ContainerName+items[j].Command
		}
		return items[i].CPUPercent > items[j].CPUPercent
	})
	return items
}
