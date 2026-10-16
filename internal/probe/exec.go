package probe

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"

	"github.com/cowhorse05/KubeNode-Observer/internal/bpfgen"
)

const taskCommLen = 16

type EventType uint32

const (
	EventTypeExec EventType = 1
	EventTypeExit EventType = 2
)

// ExecEvent represents a process lifecycle event emitted by the kernel.
type ExecEvent struct {
	TimestampNS uint64
	CgroupID    uint64
	PID         uint32
	TGID        uint32
	PPID        uint32
	Type        EventType
	ExitCode    int32
	Comm        [taskCommLen]byte
	Reserved    uint32
}

func (e ExecEvent) Command() string {
	if index := bytes.IndexByte(e.Comm[:], 0); index >= 0 {
		return string(e.Comm[:index])
	}
	return string(e.Comm[:])
}

func (e ExecEvent) Kind() string {
	if e.Type == EventTypeExit {
		return "exit"
	}
	return "exec"
}

// ExecProbe owns the loaded eBPF objects and the tracepoint attachment.
type ExecProbe struct {
	objects bpfgen.ExecObjects
	links   []link.Link
	reader  *ringbuf.Reader
	close   sync.Once
}

func NewExecProbe() (*ExecProbe, error) {
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("remove memlock limit: %w", err)
	}

	probe := &ExecProbe{}
	if err := bpfgen.LoadExecObjects(&probe.objects, nil); err != nil {
		return nil, fmt.Errorf("load exec eBPF objects: %w", err)
	}

	execTracepoint, err := link.Tracepoint("sched", "sched_process_exec", probe.objects.TraceExec, nil)
	if err != nil {
		probe.objects.Close()
		return nil, fmt.Errorf("attach sched_process_exec tracepoint: %w", err)
	}
	probe.links = append(probe.links, execTracepoint)

	exitTracepoint, err := link.Tracepoint("sched", "sched_process_exit", probe.objects.TraceExit, nil)
	if err != nil {
		execTracepoint.Close()
		probe.objects.Close()
		return nil, fmt.Errorf("attach sched_process_exit tracepoint: %w", err)
	}
	probe.links = append(probe.links, exitTracepoint)

	reader, err := ringbuf.NewReader(probe.objects.ExecEvents)
	if err != nil {
		for _, current := range probe.links {
			current.Close()
		}
		probe.objects.Close()
		return nil, fmt.Errorf("open exec event ring buffer: %w", err)
	}
	probe.reader = reader

	return probe, nil
}

func (p *ExecProbe) LostEvents() (uint64, error) {
	key := uint32(0)
	var lost uint64
	if err := p.objects.EventStats.Lookup(&key, &lost); err != nil {
		return 0, fmt.Errorf("read lost event count: %w", err)
	}
	return lost, nil
}

func (p *ExecProbe) Run(ctx context.Context, handle func(ExecEvent)) error {
	go func() {
		<-ctx.Done()
		_ = p.reader.Close()
	}()

	for {
		record, err := p.reader.Read()
		if err != nil {
			if errors.Is(err, ringbuf.ErrClosed) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read exec event: %w", err)
		}

		var event ExecEvent
		if err := binary.Read(bytes.NewReader(record.RawSample), binary.LittleEndian, &event); err != nil {
			return fmt.Errorf("decode exec event: %w", err)
		}
		handle(event)
	}
}

func (p *ExecProbe) Close() error {
	var closeErr error
	p.close.Do(func() {
		if p.reader != nil {
			if err := p.reader.Close(); err != nil && !errors.Is(err, ringbuf.ErrClosed) {
				closeErr = err
			}
		}
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
