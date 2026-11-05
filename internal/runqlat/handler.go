package runqlat

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type Source interface {
	Snapshot() ([]probe.RunQLatHistogram, error)
}

type Resolver interface {
	Resolve(cgroupID uint64) (topology.Container, string, bool)
}

type Bucket struct {
	LowerUS uint64 `json:"lowerUs"`
	UpperUS uint64 `json:"upperUs"`
	Count   uint64 `json:"count"`
}

type Latency struct {
	Count     uint64   `json:"count"`
	AverageUS float64  `json:"averageUs"`
	P50US     uint64   `json:"p50Us"`
	P95US     uint64   `json:"p95Us"`
	P99US     uint64   `json:"p99Us"`
	MaxUS     uint64   `json:"maxUs"`
	Buckets   []Bucket `json:"buckets,omitempty"`
}

type Process struct {
	Namespace      string `json:"namespace"`
	PodName        string `json:"podName"`
	PodUID         string `json:"podUid"`
	ContainerName  string `json:"containerName"`
	ContainerID    string `json:"containerId"`
	CgroupID       uint64 `json:"cgroupId"`
	PID            uint32 `json:"pid"`
	TGID           uint32 `json:"tgid"`
	Command        string `json:"command"`
	MetadataSource string `json:"metadataSource"`
	Latency
}

type Response struct {
	StartedAt       time.Time `json:"startedAt"`
	DurationSeconds float64   `json:"durationSeconds"`
	Namespace       string    `json:"namespace,omitempty"`
	PodName         string    `json:"podName,omitempty"`
	Latency
	Processes       []Process `json:"processes"`
	UnresolvedTasks int       `json:"unresolvedTasks"`
}

type Handler struct {
	source   Source
	resolver Resolver
	traceMu  sync.Mutex
}

func NewHandler(source Source, resolver Resolver) *Handler {
	return &Handler{source: source, resolver: resolver}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	duration := 10 * time.Second
	if value := request.URL.Query().Get("duration"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 100*time.Millisecond || parsed > 5*time.Minute {
			http.Error(writer, "duration must be between 100ms and 5m", http.StatusBadRequest)
			return
		}
		duration = parsed
	}

	h.traceMu.Lock()
	defer h.traceMu.Unlock()
	startedAt := time.Now()
	before, err := h.source.Snapshot()
	if err != nil {
		http.Error(writer, fmt.Sprintf("read initial run queue histogram: %v", err), http.StatusInternalServerError)
		return
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-request.Context().Done():
		return
	case <-timer.C:
	}
	after, err := h.source.Snapshot()
	if err != nil {
		http.Error(writer, fmt.Sprintf("read final run queue histogram: %v", err), http.StatusInternalServerError)
		return
	}

	response := buildResponse(
		startedAt,
		time.Since(startedAt),
		request.URL.Query().Get("namespace"),
		request.URL.Query().Get("pod"),
		before,
		after,
		h.resolver,
	)
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(response)
}

type histogramKey struct {
	cgroupID uint64
	tgid     uint32
	pid      uint32
}

type processKey struct {
	cgroupID uint64
	tgid     uint32
}

type delta struct {
	totalNS uint64
	count   uint64
	command string
	buckets [probe.RunQLatBuckets]uint64
}

func buildResponse(startedAt time.Time, duration time.Duration, namespace, pod string, before, after []probe.RunQLatHistogram, resolver Resolver) Response {
	previous := make(map[histogramKey]probe.RunQLatHistogram, len(before))
	for _, histogram := range before {
		previous[histogramKey{histogram.CgroupID, histogram.TGID, histogram.PID}] = histogram
	}

	processDeltas := make(map[processKey]delta)
	processIdentity := make(map[processKey]resolvedProcess)
	unresolved := 0
	for _, current := range after {
		key := histogramKey{current.CgroupID, current.TGID, current.PID}
		old := previous[key]
		currentDelta := delta{command: current.Command()}
		currentDelta.totalNS = counterDelta(current.TotalNS, old.TotalNS)
		for index := range current.Buckets {
			currentDelta.buckets[index] = counterDelta(current.Buckets[index], old.Buckets[index])
			currentDelta.count += currentDelta.buckets[index]
		}
		if currentDelta.count == 0 {
			continue
		}

		container, source, ok := resolver.Resolve(current.CgroupID)
		if !ok {
			unresolved++
			continue
		}
		if namespace != "" && container.Namespace != namespace {
			continue
		}
		if pod != "" && container.PodName != pod {
			continue
		}

		groupKey := processKey{cgroupID: current.CgroupID, tgid: current.TGID}
		aggregated := processDeltas[groupKey]
		aggregated.totalNS += currentDelta.totalNS
		aggregated.count += currentDelta.count
		if aggregated.command == "" || current.PID == current.TGID {
			aggregated.command = currentDelta.command
		}
		for index := range aggregated.buckets {
			aggregated.buckets[index] += currentDelta.buckets[index]
		}
		processDeltas[groupKey] = aggregated
		processIdentity[groupKey] = resolvedProcess{container: container, source: source}
	}

	response := Response{
		StartedAt:       startedAt,
		DurationSeconds: duration.Seconds(),
		Namespace:       namespace,
		PodName:         pod,
		Processes:       make([]Process, 0, len(processDeltas)),
		UnresolvedTasks: unresolved,
	}
	var overall delta
	for key, processDelta := range processDeltas {
		identity := processIdentity[key]
		latency := summarize(processDelta)
		response.Processes = append(response.Processes, Process{
			Namespace:      identity.container.Namespace,
			PodName:        identity.container.PodName,
			PodUID:         identity.container.PodUID,
			ContainerName:  identity.container.Name,
			ContainerID:    identity.container.ID,
			CgroupID:       key.cgroupID,
			PID:            key.tgid,
			TGID:           key.tgid,
			Command:        processDelta.command,
			MetadataSource: identity.source,
			Latency:        latency,
		})
		overall.totalNS += processDelta.totalNS
		overall.count += processDelta.count
		for index := range overall.buckets {
			overall.buckets[index] += processDelta.buckets[index]
		}
	}
	sort.Slice(response.Processes, func(i, j int) bool {
		if response.Processes[i].P99US == response.Processes[j].P99US {
			return response.Processes[i].AverageUS > response.Processes[j].AverageUS
		}
		return response.Processes[i].P99US > response.Processes[j].P99US
	})
	response.Latency = summarize(overall)
	return response
}

type resolvedProcess struct {
	container topology.Container
	source    string
}

func counterDelta(current, previous uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

func summarize(value delta) Latency {
	result := Latency{Count: value.count, Buckets: make([]Bucket, 0)}
	if value.count == 0 {
		return result
	}
	result.AverageUS = float64(value.totalNS) / float64(value.count) / 1000
	result.P50US = percentile(value.buckets, value.count, 50)
	result.P95US = percentile(value.buckets, value.count, 95)
	result.P99US = percentile(value.buckets, value.count, 99)
	result.MaxUS = percentile(value.buckets, value.count, 100)
	for index, count := range value.buckets {
		if count == 0 {
			continue
		}
		lower, upper := bucketBounds(index)
		result.Buckets = append(result.Buckets, Bucket{LowerUS: lower, UpperUS: upper, Count: count})
	}
	return result
}

// SummarizeHistogram reuses the same bucket/percentile semantics for Host
// threads. Counts come from histogram buckets, not a separate non-atomic map.
func SummarizeHistogram(totalNS uint64, buckets [probe.RunQLatBuckets]uint64) Latency {
	value := delta{totalNS: totalNS, buckets: buckets}
	for _, count := range buckets {
		value.count += count
	}
	return summarize(value)
}

func percentile(buckets [probe.RunQLatBuckets]uint64, count uint64, percent uint64) uint64 {
	target := (count*percent + 99) / 100
	var cumulative uint64
	for index, bucketCount := range buckets {
		cumulative += bucketCount
		if cumulative >= target {
			_, upper := bucketBounds(index)
			return upper
		}
	}
	return 0
}

func bucketBounds(index int) (uint64, uint64) {
	if index == 0 {
		return 1, 1
	}
	if index >= 63 {
		return uint64(1) << 63, math.MaxUint64
	}
	return uint64(1) << index, (uint64(1) << (index + 1)) - 1
}
