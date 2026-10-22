package compute

import (
	"golang.org/x/sys/unix"
	"time"
)

func bootElapsed() (time.Duration, error) {
	var ts unix.Timespec
	e := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts)
	return time.Duration(ts.Sec)*time.Second + time.Duration(ts.Nsec), e
}
