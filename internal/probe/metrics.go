package probe

import (
	"fmt"

	"github.com/cilium/ebpf"
)

// BPFMapStats describes kernel-accounted memory and logical occupancy for one map.
type BPFMapStats struct {
	Name       string
	Type       string
	Entries    uint64
	MaxEntries uint32
	Memlock    uint64
}

// MapStatsSource is implemented by probes which expose their owned maps.
type MapStatsSource interface {
	MapStats() ([]BPFMapStats, error)
}

func readMapStats(name string, current *ebpf.Map, countEntries bool) (BPFMapStats, error) {
	info, err := current.Info()
	if err != nil {
		return BPFMapStats{}, fmt.Errorf("read %s map info: %w", name, err)
	}
	result := BPFMapStats{
		Name:       name,
		Type:       info.Type.String(),
		MaxEntries: info.MaxEntries,
	}
	if memlock, available := info.Memlock(); available {
		result.Memlock = memlock
	}
	if !countEntries {
		return result, nil
	}

	key := make([]byte, info.KeySize)
	value := make([]byte, info.ValueSize)
	iterator := current.Iterate()
	for iterator.Next(key, value) {
		result.Entries++
	}
	if err := iterator.Err(); err != nil {
		return BPFMapStats{}, fmt.Errorf("count %s map entries: %w", name, err)
	}
	return result, nil
}
