package cpu

import (
	"time"

	"github.com/cowhorse05/KubeNode-Observer/internal/probe"
	"github.com/cowhorse05/KubeNode-Observer/internal/telemetry"
)

// TimedSource distinguishes diagnostic scans from production sampling.
type TimedSource struct {
	Source    RuntimeSource
	Recorder  *telemetry.Recorder
	Operation telemetry.Operation
}

func (s TimedSource) Snapshot() (probe.CPUSnapshot, error) {
	defer s.Recorder.Observe(s.Operation, time.Now())
	return s.Source.Snapshot()
}
