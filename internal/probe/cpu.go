package probe

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/cilium/ebpf/link"
	"github.com/cowhorse05/KubeNode-Observer/internal/bpfgen"
)

const cpuCommLen = 16

type CPURuntime struct {
	CgroupID uint64
	TGID     uint32
	PID      uint32
	Runtime  uint64
	Comm     [cpuCommLen]byte
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
	Runtime uint64
	Comm    [cpuCommLen]byte
}

type CPUProbe struct {
	objects bpfgen.CPUObjects
	link    link.Link
	close   sync.Once
}

func NewCPUProbe() (*CPUProbe, error) {
	probe := &CPUProbe{}
	if err := bpfgen.LoadCPUObjects(&probe.objects, nil); err != nil {
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

func (p *CPUProbe) Snapshot() ([]CPURuntime, error) {
	iterator := p.objects.CpuRuntime.Iterate()
	result := make([]CPURuntime, 0)
	var key cpuKey
	var value cpuValue
	for iterator.Next(&key, &value) {
		result = append(result, CPURuntime{
			CgroupID: key.CgroupID,
			TGID:     key.TGID,
			PID:      key.PID,
			Runtime:  value.Runtime,
			Comm:     value.Comm,
		})
	}
	if err := iterator.Err(); err != nil {
		return nil, fmt.Errorf("iterate CPU runtime map: %w", err)
	}
	return result, nil
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
