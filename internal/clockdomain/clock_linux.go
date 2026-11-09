// Package clockdomain reads clocks; it never sets or disciplines any clock.
package clockdomain

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type Identity struct {
	BootID        string `json:"bootId"`
	TimeNamespace string `json:"timeNamespace"`
}
type LinuxClock struct{}

func Read(id int32) (int64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(id, &ts); err != nil {
		return 0, err
	}
	if ts.Sec < 0 || ts.Sec > 9223372035 || ts.Nsec < 0 || ts.Nsec >= 1e9 {
		return 0, fmt.Errorf("clock outside supported range")
	}
	return ts.Sec*1e9 + ts.Nsec, nil
}
func (LinuxClock) Now() (int64, error) { return Read(unix.CLOCK_MONOTONIC) }
func (LinuxClock) Identity() (Identity, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Identity{}, err
	}
	ns, err := os.Readlink("/proc/self/ns/time")
	if os.IsNotExist(err) {
		ns = "unavailable"
		err = nil
	}
	if err != nil {
		return Identity{}, err
	}
	return Identity{BootID: strings.TrimSpace(string(b)), TimeNamespace: ns}, nil
}
