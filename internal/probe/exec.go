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

// ExecEvent is emitted after a process successfully replaces its executable.
type ExecEvent struct {
	TimestampNS uint64
	CgroupID    uint64
	PID         uint32
	TGID        uint32
	Comm        [taskCommLen]byte
}

func (e ExecEvent) Command() string {
	if index := bytes.IndexByte(e.Comm[:], 0); index >= 0 {
		return string(e.Comm[:index])
	}
	return string(e.Comm[:])
}

// ExecProbe owns the loaded eBPF objects and the tracepoint attachment.
type ExecProbe struct {
	objects bpfgen.ExecObjects
	link    link.Link
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

	tracepoint, err := link.Tracepoint("sched", "sched_process_exec", probe.objects.TraceExec, nil)
	if err != nil {
		probe.objects.Close()
		return nil, fmt.Errorf("attach sched_process_exec tracepoint: %w", err)
	}
	probe.link = tracepoint

	reader, err := ringbuf.NewReader(probe.objects.ExecEvents)
	if err != nil {
		probe.link.Close()
		probe.objects.Close()
		return nil, fmt.Errorf("open exec event ring buffer: %w", err)
	}
	probe.reader = reader

	return probe, nil
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
		if p.link != nil {
			if err := p.link.Close(); closeErr == nil && err != nil {
				closeErr = err
			}
		}
		if err := p.objects.Close(); closeErr == nil && err != nil {
			closeErr = err
		}
	})
	return closeErr
}
