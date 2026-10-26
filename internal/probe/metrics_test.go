package probe

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cilium/ebpf"
)

func TestCountMapKeys(t *testing.T) {
	for _, size := range []uint32{0, 1, 8} {
		calls := uint32(0)
		count, err := countMapKeys(4, 8, func(key, out any) error {
			if calls == 0 && key != nil {
				t.Fatal("first key must be nil")
			}
			calls++
			if calls > size {
				return fmt.Errorf("eof: %w", ebpf.ErrKeyNotExist)
			}
			return nil
		})
		if err != nil || count != uint64(size) || calls != size+1 {
			t.Fatalf("size %d: count=%d calls=%d err=%v", size, count, calls, err)
		}
	}
}

func TestCountMapKeysIsBoundedAndPropagatesErrors(t *testing.T) {
	calls := 0
	count, err := countMapKeys(4, 8, func(any, any) error { calls++; return nil })
	if !errors.Is(err, ebpf.ErrIterationAborted) || count != 0 || calls != 9 {
		t.Fatalf("unbounded/incomplete count: %d %d %v", count, calls, err)
	}
	failure := errors.New("permission denied")
	if count, err := countMapKeys(4, 8, func(any, any) error { return failure }); count != 0 || !errors.Is(err, failure) {
		t.Fatalf("swallowed failure: %d %v", count, err)
	}
	if _, err := countMapKeys(0, 8, nil); err == nil {
		t.Fatal("zero key accepted")
	}
}
