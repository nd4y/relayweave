package worker

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func budgetRemaining(usedFile, limitFile string) (int64, int64, bool) {
	usedRaw, e := os.ReadFile(usedFile)
	if e != nil {
		return 0, 0, false
	}
	limitRaw, e := os.ReadFile(limitFile)
	if e != nil {
		return 0, 0, false
	}
	used, e1 := strconv.ParseInt(strings.TrimSpace(string(usedRaw)), 10, 64)
	limit, e2 := strconv.ParseInt(strings.TrimSpace(string(limitRaw)), 10, 64)
	if e1 != nil || e2 != nil || used < 0 || limit <= 0 || limit > 1<<60 {
		return 0, 0, false
	}
	return max(int64(0), limit-used), limit, true
}

func engineHeadroom() error {
	for _, pair := range [][2]string{{"/sys/fs/cgroup/memory.current", "/sys/fs/cgroup/memory.max"}, {"/sys/fs/cgroup/memory/memory.usage_in_bytes", "/sys/fs/cgroup/memory/memory.limit_in_bytes"}} {
		if remaining, limit, ok := budgetRemaining(pair[0], pair[1]); ok {
			if remaining < max(limit/5, int64(64<<20)) {
				return fmt.Errorf("engine_memory_headroom")
			}
			break
		}
	}
	for _, pair := range [][2]string{{"/sys/fs/cgroup/pids.current", "/sys/fs/cgroup/pids.max"}, {"/sys/fs/cgroup/pids/pids.current", "/sys/fs/cgroup/pids/pids.max"}} {
		if remaining, limit, ok := budgetRemaining(pair[0], pair[1]); ok {
			if remaining < max(limit/5, int64(48)) {
				return fmt.Errorf("engine_pid_headroom")
			}
			break
		}
	}
	var limit syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit) == nil && limit.Cur < 1<<60 {
		if files, e := os.ReadDir("/proc/self/fd"); e == nil && uint64(len(files))+max(uint64(64), limit.Cur/10) >= limit.Cur {
			return fmt.Errorf("engine_fd_headroom")
		}
	}
	return nil
}
