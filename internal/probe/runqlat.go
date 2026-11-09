package probe

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cowhorse05/KubeNode-Observer/internal/bpfgen"
)

const RunQLatBuckets = 64

type RunQLatHistogram struct {
	CgroupID uint64
	TGID     uint32
	PID      uint32
	TotalNS  uint64
	Count    uint64
	Comm     [taskCommLen]byte
	Buckets  [RunQLatBuckets]uint64
}

func (h RunQLatHistogram) Command() string {
	if index := bytes.IndexByte(h.Comm[:], 0); index >= 0 {
		return string(h.Comm[:index])
	}
	return string(h.Comm[:])
}

type RunQLatProbe struct {
	objects bpfgen.RunQLatObjects
	links   []link.Link
	close   sync.Once
}

// TaskIdentity is the last switch-out identity already maintained by runqlat.
// It is a scheduler observation, not an atomic procfs/current-cgroup guarantee.
type TaskIdentity struct {
	CgroupID uint64
	TGID     uint32
}

func (p *RunQLatProbe) TaskIdentity(pid uint32) (TaskIdentity, bool, error) {
	var value bpfgen.RunQLatTaskIdentity
	err := p.objects.TaskIdentity.Lookup(pid, &value)
	if errors.Is(err, ebpf.ErrKeyNotExist) {
		return TaskIdentity{}, false, nil
	}
	if err != nil {
		return TaskIdentity{}, false, fmt.Errorf("read scheduled task identity: %w", err)
	}
	return TaskIdentity{CgroupID: value.CgroupId, TGID: value.Tgid}, true, nil
}

func NewRunQLatProbe() (*RunQLatProbe, error) {
	probe := &RunQLatProbe{}
	if err := bpfgen.LoadRunQLatObjects(&probe.objects, nil); err != nil {
		return nil, fmt.Errorf("load run queue latency eBPF objects: %w", err)
	}

	attachments := []struct {
		name        string
		programName string
		program     *ebpf.Program
	}{
		{name: "sched_wakeup", programName: "sched_wakeup", program: probe.objects.TraceWakeup},
		{name: "sched_wakeup_new", programName: "sched_wakeup_new", program: probe.objects.TraceWakeupNew},
		{name: "sched_switch", programName: "sched_switch", program: probe.objects.TraceRunqlat},
	}
	for _, attachment := range attachments {
		attached, err := link.AttachRawTracepoint(link.RawTracepointOptions{
			Name:    attachment.name,
			Program: attachment.program,
		})
		if err != nil {
			probe.Close()
			return nil, fmt.Errorf("attach %s raw tracepoint: %w", attachment.programName, err)
		}
		probe.links = append(probe.links, attached)
	}
	return probe, nil
}

func (p *RunQLatProbe) Snapshot() ([]RunQLatHistogram, error) {
	histograms := make(map[bpfgen.RunQLatLatencyKey]*RunQLatHistogram)
	statsIterator := p.objects.LatencyStats.Iterate()
	var key bpfgen.RunQLatLatencyKey
	var stats bpfgen.RunQLatLatencyStats
	for statsIterator.Next(&key, &stats) {
		histogram := &RunQLatHistogram{
			CgroupID: key.CgroupId,
			TGID:     key.Tgid,
			PID:      key.Pid,
			TotalNS:  stats.TotalNs,
			Count:    stats.Count,
		}
		for index, character := range stats.Comm {
			histogram.Comm[index] = byte(character)
		}
		histograms[key] = histogram
	}
	if err := statsIterator.Err(); err != nil {
		return nil, fmt.Errorf("iterate run queue latency stats: %w", err)
	}

	histogramIterator := p.objects.LatencyHistogram.Iterate()
	var histogramKey bpfgen.RunQLatHistogramKey
	var count uint64
	for histogramIterator.Next(&histogramKey, &count) {
		histogram := histograms[histogramKey.Process]
		if histogram != nil && histogramKey.Bucket < RunQLatBuckets {
			histogram.Buckets[histogramKey.Bucket] = count
		}
	}
	if err := histogramIterator.Err(); err != nil {
		return nil, fmt.Errorf("iterate run queue latency histogram: %w", err)
	}

	result := make([]RunQLatHistogram, 0, len(histograms))
	for _, histogram := range histograms {
		result = append(result, *histogram)
	}
	return result, nil
}

func (p *RunQLatProbe) MapStats() ([]BPFMapStats, error) {
	definitions := []struct {
		name  string
		value *ebpf.Map
	}{
		{name: "runqlat_runnable_since", value: p.objects.RunnableSince},
		{name: "runqlat_task_identity", value: p.objects.TaskIdentity},
		{name: "runqlat_latency_stats", value: p.objects.LatencyStats},
		{name: "runqlat_latency_histogram", value: p.objects.LatencyHistogram},
	}
	result := make([]BPFMapStats, 0, len(definitions))
	for _, definition := range definitions {
		stats, err := readMapStats(definition.name, definition.value, true)
		if err != nil {
			return nil, err
		}
		result = append(result, stats)
	}
	return result, nil
}

func (p *RunQLatProbe) Close() error {
	var closeErr error
	p.close.Do(func() {
		for _, current := range p.links {
			if err := current.Close(); closeErr == nil && err != nil {
				closeErr = err
			}
		}
		if err := p.objects.Close(); closeErr == nil && err != nil {
			closeErr = err
		}
	})
	return closeErr
}
