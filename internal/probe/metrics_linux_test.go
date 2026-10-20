package probe

import (
	"os"
	"testing"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/rlimit"
)

// Opt-in real-kernel check, run as root/CAP_BPF in the Linux development host.
func TestMapStatsKernel(t *testing.T) {
	if os.Getenv("KNO_BPF_TEST") != "1" {
		t.Skip("set KNO_BPF_TEST=1 with BPF privileges")
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []ebpf.MapType{ebpf.LRUHash, ebpf.Array} {
		m, err := ebpf.NewMap(&ebpf.MapSpec{Type: kind, KeySize: 4, ValueSize: 8, MaxEntries: 16})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = m.Close() })
		for key := uint32(0); key < 16; key++ {
			value := uint64(key + 1)
			if err := m.Put(key, value); err != nil {
				t.Fatal(err)
			}
		}
		stats, err := readMapStats("test", m, true)
		if err != nil || stats.Entries != 16 {
			t.Fatalf("%s: %+v %v", kind, stats, err)
		}
		var value uint64
		if err := m.Lookup(uint32(8), &value); err != nil || value != 9 {
			t.Fatalf("count mutated map: %d %v", value, err)
		}
		if kind == ebpf.LRUHash {
			for key := uint32(0); key < 16; key++ {
				if err := m.Delete(key); err != nil {
					t.Fatal(err)
				}
			}
			stats, err = readMapStats("empty", m, true)
			if err != nil || stats.Entries != 0 {
				t.Fatalf("empty: %+v %v", stats, err)
			}
		}
	}
}
