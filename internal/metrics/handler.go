package metrics

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/agent"
	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
)

type HealthSource interface {
	Stats() agent.HealthStats
}

type Handler struct {
	Timing   *telemetry.Recorder // Set before serving; sources are exec, CPU, runqlat.
	health   HealthSource
	maps     []probe.MapStatsSource
	procRoot string
}

func NewHandler(health HealthSource, maps []probe.MapStatsSource, procRoot string) *Handler {
	return &Handler{health: health, maps: maps, procRoot: procRoot}
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer h.Timing.Observe(telemetry.MetricsScrape, time.Now())

	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	stats := h.health.Stats()
	writeMetric(writer, "kno_agent_events_seen_total", "Process lifecycle events consumed by the agent.", "counter", float64(stats.EventsSeen))
	writeMetric(writer, "kno_agent_events_lost_total", "Process lifecycle events dropped in the kernel ring buffer.", "counter", float64(stats.EventsLost))

	collectionErrors := 0
	if cpuNS, err := readProcessCPU(h.procRoot); err == nil {
		writeMetric(writer, "kno_agent_cpu_seconds_total", "CPU time consumed by the agent process.", "counter", float64(cpuNS)/1e9)
	} else {
		collectionErrors++
	}
	if rss, err := readRSS(h.procRoot + "/self/statm"); err == nil {
		writeMetric(writer, "kno_agent_resident_memory_bytes", "Resident memory used by the agent process.", "gauge", float64(rss))
	} else {
		collectionErrors++
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	writeMetric(writer, "kno_agent_go_heap_alloc_bytes", "Currently allocated Go heap bytes.", "gauge", float64(memory.HeapAlloc))

	writeMetricHeader(writer, "kno_agent_bpf_map_entries", "Current logical entries in an eBPF map; ring buffers report zero.", "gauge")
	writeMetricHeader(writer, "kno_agent_bpf_map_max_entries", "Configured eBPF map entry capacity; ring buffers use bytes.", "gauge")
	writeMetricHeader(writer, "kno_agent_bpf_map_memlock_bytes", "Kernel-reported locked memory for an eBPF map.", "gauge")

	for index, source := range h.maps {
		started := time.Now()
		maps, err := source.MapStats()
		if index < 3 {
			h.Timing.Observe(telemetry.MetricsExec+telemetry.Operation(index), started)
		}
		if err != nil {
			collectionErrors++
			continue
		}
		for _, current := range maps {
			labels := fmt.Sprintf("map=\"%s\",type=\"%s\"", escapeLabel(current.Name), escapeLabel(current.Type))
			writeLabeledMetric(writer, "kno_agent_bpf_map_entries", labels, float64(current.Entries))
			writeLabeledMetric(writer, "kno_agent_bpf_map_max_entries", labels, float64(current.MaxEntries))
			writeLabeledMetric(writer, "kno_agent_bpf_map_memlock_bytes", labels, float64(current.Memlock))
		}
	}
	writeMetric(writer, "kno_agent_metrics_collection_errors", "Metric sources which could not be read during this scrape.", "gauge", float64(collectionErrors))
}

func readUintFile(path string) (uint64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return 0, fmt.Errorf("%s is empty", path)
	}
	return strconv.ParseUint(fields[0], 10, 64)
}

func readProcessCPU(procRoot string) (uint64, error) {
	paths, err := filepath.Glob(filepath.Join(procRoot, "self", "task", "*", "schedstat"))
	if err != nil {
		return 0, err
	}
	if len(paths) == 0 {
		return 0, fmt.Errorf("no task schedstat files found")
	}
	var total uint64
	for _, path := range paths {
		value, err := readUintFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total += value
	}
	return total, nil
}

func readRSS(path string) (uint64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	scanner.Split(bufio.ScanWords)
	if !scanner.Scan() || !scanner.Scan() {
		return 0, fmt.Errorf("%s does not contain an RSS field", path)
	}
	pages, err := strconv.ParseUint(scanner.Text(), 10, 64)
	if err != nil {
		return 0, err
	}
	return pages * uint64(os.Getpagesize()), nil
}

func writeMetric(writer http.ResponseWriter, name, help, metricType string, value float64) {
	writeMetricHeader(writer, name, help, metricType)
	fmt.Fprintf(writer, "%s %g\n", name, value)
}

func writeMetricHeader(writer http.ResponseWriter, name, help, metricType string) {
	fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func writeLabeledMetric(writer http.ResponseWriter, name, labels string, value float64) {
	fmt.Fprintf(writer, "%s{%s} %g\n", name, labels, value)
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}
