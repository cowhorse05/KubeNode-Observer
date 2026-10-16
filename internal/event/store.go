package event

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/topology"
)

type Resolver interface {
	Resolve(cgroupID uint64) (topology.Container, string, bool)
}

type WorkloadIdentity struct {
	Namespace        string `json:"namespace"`
	PodName          string `json:"podName"`
	PodUID           string `json:"podUid"`
	KubernetesPodUID string `json:"kubernetesPodUid,omitempty"`
	ContainerName    string `json:"containerName"`
	ContainerID      string `json:"containerId"`
	Image            string `json:"image"`
}

type Record struct {
	Sequence       uint64            `json:"sequence"`
	Type           string            `json:"type"`
	TimestampNS    uint64            `json:"timestampNs"`
	CgroupID       uint64            `json:"cgroupId"`
	PID            uint32            `json:"pid"`
	TGID           uint32            `json:"tgid"`
	PPID           uint32            `json:"ppid"`
	Command        string            `json:"comm"`
	ExitCode       int32             `json:"exitCode,omitempty"`
	ExitSignal     int32             `json:"exitSignal,omitempty"`
	RawExitCode    int32             `json:"rawExitCode,omitempty"`
	MetadataSource string            `json:"metadataSource"`
	Workload       *WorkloadIdentity `json:"workload,omitempty"`
}

type Response struct {
	Events []Record `json:"events"`
}

type Store struct {
	mu       sync.RWMutex
	events   []Record
	next     uint64
	capacity int
	resolver Resolver
}

func NewStore(capacity int, resolver Resolver) *Store {
	return &Store{capacity: capacity, resolver: resolver, events: make([]Record, 0, capacity)}
}

func (s *Store) Add(kernel probe.ExecEvent) Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.next++
	record := Record{
		Sequence:       s.next,
		Type:           kernel.Kind(),
		TimestampNS:    kernel.TimestampNS,
		CgroupID:       kernel.CgroupID,
		PID:            kernel.PID,
		TGID:           kernel.TGID,
		PPID:           kernel.PPID,
		Command:        kernel.Command(),
		MetadataSource: "unresolved",
	}
	if kernel.Type == probe.EventTypeExit {
		raw := uint32(kernel.ExitCode)
		record.RawExitCode = kernel.ExitCode
		record.ExitCode = int32((raw >> 8) & 0xff)
		record.ExitSignal = int32(raw & 0x7f)
	}
	s.enrich(&record)
	if len(s.events) == s.capacity {
		copy(s.events, s.events[1:])
		s.events[len(s.events)-1] = record
	} else {
		s.events = append(s.events, record)
	}
	return record
}

func (s *Store) enrich(record *Record) {
	if s.resolver == nil || record.Workload != nil {
		return
	}
	container, source, ok := s.resolver.Resolve(record.CgroupID)
	if !ok {
		return
	}
	record.MetadataSource = source
	record.Workload = &WorkloadIdentity{
		Namespace:        container.Namespace,
		PodName:          container.PodName,
		PodUID:           container.PodUID,
		KubernetesPodUID: container.KubernetesPodUID,
		ContainerName:    container.Name,
		ContainerID:      container.ID,
		Image:            container.Image,
	}
}

func (s *Store) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 100
	if value := request.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 1000 {
			http.Error(writer, "limit must be between 1 and 1000", http.StatusBadRequest)
			return
		}
		limit = parsed
	}

	response := Response{Events: s.list(
		request.URL.Query().Get("namespace"),
		request.URL.Query().Get("pod"),
		request.URL.Query().Get("type"),
		limit,
	)}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(response)
}

func (s *Store) list(namespace, pod, eventType string, limit int) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Record, 0, limit)
	for index := len(s.events) - 1; index >= 0 && len(result) < limit; index-- {
		record := &s.events[index]
		s.enrich(record)
		if eventType != "" && record.Type != eventType {
			continue
		}
		if namespace != "" && (record.Workload == nil || record.Workload.Namespace != namespace) {
			continue
		}
		if pod != "" && (record.Workload == nil || record.Workload.PodName != pod) {
			continue
		}
		result = append(result, *record)
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}
