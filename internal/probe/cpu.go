package probe

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cowhorse05/KubeNode-Observer/internal/bpfgen"
)

const cpuCommLen = 16

type CgroupRuntime struct {
	CgroupID uint64
	Runtime  uint64
}

// Process detail is bounded independently of container totals. CapturedAt is
// taken after the small totals scan, not before a potentially slow detail scan.
type CPUSnapshot struct {
	Processes         []CPURuntime
	Totals            []CgroupRuntime
	CapturedAt        time.Time
	DetailsCapturedAt time.Time
}

type CPURuntime struct {
	CgroupID      uint64
	TGID          uint32
	PID           uint32
	Runtime       uint64
	Comm          [cpuCommLen]byte
	NamespacePID  uint32
	NamespaceTGID uint32
}

func (r CPURuntime) Command() string {
	if index := bytes.IndexByte(r.Comm[:], 0); index >= 0 {
		return string(r.Comm[:index])
	}
	return string(r.Comm[:])
}

type cpuKey struct {
	CgroupID uint64
	TGID     uint32
	PID      uint32
}

type cpuValue struct {
	Runtime       uint64
	Comm          [cpuCommLen]byte
	NamespacePID  uint32
	NamespaceTGID uint32
}

type CPUProbe struct {
	objects bpfgen.CPUObjects
	link    link.Link
	close   sync.Once
}

func NewCPUProbe() (*CPUProbe, error) {
	probe := &CPUProbe{}
	spec, err := bpfgen.LoadCPU()
	if err != nil {
		return nil, fmt.Errorf("load CPU spec: %w", err)
	}
	info, err := os.Stat("/proc/self/ns/pid")
	if err != nil {
		return nil, fmt.Errorf("stat Host PID namespace: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("PID namespace stat unsupported")
	}
	if err := spec.RewriteConstants(map[string]interface{}{"pidns_dev": uint64(stat.Dev), "pidns_ino": stat.Ino}); err != nil {
		return nil, fmt.Errorf("configure Host PID namespace: %w", err)
	}
	if err := spec.LoadAndAssign(&probe.objects, nil); err != nil {
		return nil, fmt.Errorf("load CPU eBPF objects: %w", err)
	}

	attached, err := link.AttachRawTracepoint(link.RawTracepointOptions{
		Name:    "sched_switch",
		Program: probe.objects.TraceCpuRuntime,
	})
	if err != nil {
		probe.objects.Close()
		return nil, fmt.Errorf("attach sched_switch raw tracepoint: %w", err)
	}
	probe.link = attached
	return probe, nil
}

func (p *CPUProbe) Snapshot() (CPUSnapshot, error) {
	result := CPUSnapshot{Totals: make([]CgroupRuntime, 0)}
	var cgroup, total uint64
	iterator := p.objects.CgroupRuntime.Iterate()
	for iterator.Next(&cgroup, &total) {
		result.Totals = append(result.Totals, CgroupRuntime{cgroup, total})
	}
	if err := iterator.Err(); err != nil {
		return CPUSnapshot{}, fmt.Errorf("iterate cgroup runtime map: %w", err)
	}
	result.CapturedAt = time.Now()
	// Batch cursors avoid next-key traversal restarting when a short process
	// key is evicted during a scan. This is still not an atomic snapshot.
	keys, values := make([]cpuKey, 256), make([]cpuValue, 256)
	var cursor ebpf.MapBatchCursor
	for scanned := 0; scanned <= 8192; {
		n, err := p.objects.CpuRuntime.BatchLookup(&cursor, keys, values, nil)
		scanned += n
		for i := 0; i < n; i++ {
			key, value := keys[i], values[i]
			result.Processes = append(result.Processes, CPURuntime{
				CgroupID:      key.CgroupID,
				TGID:          key.TGID,
				PID:           key.PID,
				Runtime:       value.Runtime,
				Comm:          value.Comm,
				NamespacePID:  value.NamespacePID,
				NamespaceTGID: value.NamespaceTGID,
			})
		}
		if errors.Is(err, ebpf.ErrKeyNotExist) {
			result.DetailsCapturedAt = time.Now()
			return result, nil
		}
		if err != nil {
			return CPUSnapshot{}, fmt.Errorf("batch read CPU runtime map: %w", err)
		}
		if n == 0 {
			return CPUSnapshot{}, fmt.Errorf("CPU batch cursor made no progress")
		}
	}
	return CPUSnapshot{}, fmt.Errorf("batch read CPU runtime map: %w", ebpf.ErrIterationAborted)
}

func (p *CPUProbe) MapStats() ([]BPFMapStats, error) {
	starts, err := readMapStats("cpu_task_start", p.objects.TaskStart, true)
	if err != nil {
		return nil, err
	}
	stats, err := readMapStats("cpu_runtime", p.objects.CpuRuntime, true)
	if err != nil {
		return nil, err
	}
	totals, err := readMapStats("cpu_cgroup_runtime", p.objects.CgroupRuntime, true)
	if err != nil {
		return nil, err
	}
	return []BPFMapStats{starts, stats, totals}, nil
}

func (p *CPUProbe) Close() error {
	var closeErr error
	p.close.Do(func() {
		if p.link != nil {
			closeErr = p.link.Close()
		}
		if err := p.objects.Close(); closeErr == nil && err != nil {
			closeErr = err
		}
	})
	return closeErr
}
