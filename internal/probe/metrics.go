package probe

import (
	"errors"
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

	entries, err := countMapKeys(info.KeySize, info.MaxEntries, current.NextKey)
	if err != nil {
		return BPFMapStats{}, fmt.Errorf("count %s map entries: %w", name, err)
	}
	result.Entries = entries
	return result, nil
}

// Like Map.Iterate, this is a non-atomic census under concurrent mutation.
// Only keys are needed for occupancy. Do not Lookup/decode every value.
// Deletion can restart traversal, so bound work and discard incomplete counts.
func countMapKeys(keySize, capacity uint32, next func(any, any) error) (uint64, error) {
	if keySize == 0 || capacity == 0 {
		return 0, fmt.Errorf("invalid map key size/capacity")
	}
	var previous any
	cursor := any(make([]byte, keySize))
	for count := uint64(0); count <= uint64(capacity); count++ {
		err := next(previous, cursor)
		if errors.Is(err, ebpf.ErrKeyNotExist) {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
		previous = cursor
	}
	return 0, ebpf.ErrIterationAborted
}
