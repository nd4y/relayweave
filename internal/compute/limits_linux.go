package compute

import (
	"golang.org/x/sys/unix"
	"os"
	"strconv"
	"strings"
)

func processCPU() (int64, error) {
	var u unix.Rusage
	e := unix.Getrusage(unix.RUSAGE_SELF, &u)
	return (u.Utime.Sec+u.Stime.Sec)*1000 + (u.Utime.Usec+u.Stime.Usec)/1000, e
}

// Read the actual container limit, not an environment assertion or model label.
// Unsupported cgroup layouts fail closed until explicitly implemented/tested.
func EnforceLimits() error {
	raw, e := os.ReadFile("/sys/fs/cgroup/cpu.max")
	if e != nil {
		return ErrInvalid
	}
	parts := strings.Fields(string(raw))
	if len(parts) != 2 || parts[0] == "max" {
		return ErrInvalid
	}
	quota, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil {
		return e
	}
	period, e := strconv.ParseInt(parts[1], 10, 64)
	if e != nil || period <= 0 || quota <= 0 || quota > period {
		return ErrInvalid
	}
	raw, e = os.ReadFile("/sys/fs/cgroup/memory.max")
	if e != nil {
		return e
	}
	memory, e := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if e != nil || memory <= 0 || memory > 512<<20 {
		return ErrInvalid
	}
	var limit unix.Rlimit
	limit.Cur, limit.Max = 0, 0
	return unix.Setrlimit(unix.RLIMIT_CORE, &limit)
}
