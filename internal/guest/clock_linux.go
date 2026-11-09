package guest

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type LinuxClock struct{}

func (LinuxClock) Now() (int64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0, err
	}
	if ts.Sec < 0 || ts.Sec > 9223372035 || ts.Nsec < 0 || ts.Nsec >= 1e9 {
		return 0, fmt.Errorf("monotonic clock outside supported range")
	}
	return ts.Sec*1e9 + ts.Nsec, nil
}
func (LinuxClock) Identity() (ClockIdentity, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return ClockIdentity{}, err
	}
	ns, err := os.Readlink("/proc/self/ns/time")
	if os.IsNotExist(err) {
		ns = "unavailable"
		err = nil
	}
	if err != nil {
		return ClockIdentity{}, err
	}
	return ClockIdentity{BootID: strings.TrimSpace(string(b)), TimeNamespace: ns}, nil
}
