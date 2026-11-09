package clockdomain

import (
	"fmt"
	"math"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
)

// KernelClock is one private array entry + a socket-filter TEST_RUN program.
// It is never attached to a socket, tracepoint, scheduler or production map.
type KernelClock struct {
	program *ebpf.Program
	value   *ebpf.Map
}

func NewKernelClock() (*KernelClock, error) {
	m, err := ebpf.NewMap(&ebpf.MapSpec{Name: "kno_clock", Type: ebpf.Array, KeySize: 4, ValueSize: 8, MaxEntries: 1})
	if err != nil {
		return nil, err
	}
	p, err := ebpf.NewProgram(&ebpf.ProgramSpec{Name: "kno_clock", Type: ebpf.SocketFilter, License: "GPL", Instructions: asm.Instructions{
		asm.StoreImm(asm.R10, -4, 0, asm.Word), asm.LoadMapPtr(asm.R1, m.FD()), asm.Mov.Reg(asm.R2, asm.R10), asm.Add.Imm(asm.R2, -4),
		asm.FnMapLookupElem.Call(), asm.JEq.Imm(asm.R0, 0, "exit"), asm.Mov.Reg(asm.R6, asm.R0), asm.FnKtimeGetNs.Call(), asm.StoreMem(asm.R6, 0, asm.R0, asm.DWord),
		asm.Mov.Imm(asm.R0, 0).WithSymbol("exit"), asm.Return(),
	}})
	if err != nil {
		m.Close()
		return nil, err
	}
	return &KernelClock{program: p, value: m}, nil
}
func (k *KernelClock) Now() (int64, error) {
	if _, _, err := k.program.Test(make([]byte, 14)); err != nil {
		return 0, fmt.Errorf("clock BPF TEST_RUN: %w", err)
	}
	var value uint64
	if err := k.value.Lookup(uint32(0), &value); err != nil {
		return 0, err
	}
	if value == 0 || value > math.MaxInt64 {
		return 0, fmt.Errorf("invalid kernel clock sample")
	}
	return int64(value), nil
}
func (k *KernelClock) Close() { k.program.Close(); k.value.Close() }
