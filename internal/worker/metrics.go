package worker

import (
	"fmt"
	"io"
	"os"
	"proxyplatform/internal/identity"
	"proxyplatform/internal/telemetry"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

var connectBounds = []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 3, 10, 30}

type connectStats struct {
	attempts atomic.Uint64
	failures atomic.Uint64
	count    atomic.Uint64
	sumUS    atomic.Uint64
	buckets  [12]atomic.Uint64
}

func (s *connectStats) observe(d time.Duration) {
	s.count.Add(1)
	s.sumUS.Add(uint64(d.Microseconds()))
	for i, b := range connectBounds {
		if d.Seconds() <= b {
			s.buckets[i].Add(1)
		}
	}
}
func (s *connectStats) write(w io.Writer) {
	fmt.Fprintf(w, "proxy_connect_attempts_total %d\nproxy_connect_failures_total %d\n# TYPE proxy_connect_duration_seconds histogram\n", s.attempts.Load(), s.failures.Load())
	for i, b := range connectBounds {
		fmt.Fprintf(w, "proxy_connect_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(b, 'f', -1, 64), s.buckets[i].Load())
	}
	fmt.Fprintf(w, "proxy_connect_duration_seconds_bucket{le=\"+Inf\"} %d\nproxy_connect_duration_seconds_count %d\nproxy_connect_duration_seconds_sum %g\n", s.count.Load(), s.count.Load(), float64(s.sumUS.Load())/1e6)
}
func resourceMetrics(w io.Writer) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Fprintf(w, "go_goroutines %d\ngo_memstats_heap_alloc_bytes %d\nproxy_telemetry_dropped_total %d\n", runtime.NumGoroutine(), mem.HeapAlloc, telemetry.Dropped())
	if fd, e := os.ReadDir("/proc/self/fd"); e == nil {
		fmt.Fprintf(w, "process_open_fds %d\n", len(fd))
	}
	if b, e := os.ReadFile("/proc/self/statm"); e == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 1 {
			pages, _ := strconv.ParseUint(fields[1], 10, 64)
			fmt.Fprintf(w, "process_resident_memory_bytes %d\n", pages*uint64(os.Getpagesize()))
		}
	}
	var disk syscall.Statfs_t
	if syscall.Statfs("/data", &disk) == nil {
		fmt.Fprintf(w, "proxy_state_disk_free_bytes %d\n", disk.Bavail*uint64(disk.Bsize))
	}
	if syscall.Statfs("/cores", &disk) == nil {
		fmt.Fprintf(w, "proxy_core_disk_free_bytes %d\n", disk.Bavail*uint64(disk.Bsize))
	}
	dir := os.Getenv("IDENTITY_DIR")
	if dir == "" {
		dir = "/worker-identity"
	}
	for role, expiry := range identity.CertificateExpiries(dir) {
		fmt.Fprintf(w, "proxy_certificate_expiry_timestamp_seconds{role=%q} %d\n", role, expiry)
	}
}

func engineResources(w io.Writer, key string, pid int64) {
	if pid < 2 {
		return
	}
	prefix := "/proc/" + strconv.FormatInt(pid, 10)
	if b, e := os.ReadFile(prefix + "/statm"); e == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 1 {
			pages, _ := strconv.ParseUint(fields[1], 10, 64)
			fmt.Fprintf(w, "proxy_core_resident_memory_bytes{engine=%q} %d\n", key, pages*uint64(os.Getpagesize()))
		}
	}
	if fd, e := os.ReadDir(prefix + "/fd"); e == nil {
		fmt.Fprintf(w, "proxy_core_open_fds{engine=%q} %d\n", key, len(fd))
	}
	if b, e := os.ReadFile(prefix + "/schedstat"); e == nil {
		fields := strings.Fields(string(b))
		if len(fields) > 0 {
			ns, _ := strconv.ParseFloat(fields[0], 64)
			fmt.Fprintf(w, "proxy_core_cpu_seconds_total{engine=%q} %g\n", key, ns/1e9)
		}
	}
}
