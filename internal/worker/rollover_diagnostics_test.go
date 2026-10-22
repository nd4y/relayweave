package worker

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const rolloverTraceCapacity = 256

type rolloverTraceEvent struct {
	ElapsedNS   int64
	Stage       string
	Sequence    uint64
	PID         int64
	Peer        string
	PeerChanged bool
	Bytes       int
	Error       string `json:",omitempty"`
	Output      string `json:",omitempty"`
}

// Only synthetic fixture fields enter this trace: no payload/config/headers.
// TryLock keeps observation independent of all runtime locks and traffic.
type rolloverDiagnostics struct {
	started time.Time
	path    string
	mu      sync.Mutex
	ring    [rolloverTraceCapacity]rolloverTraceEvent
	next    int
	count   int
	dropped atomic.Uint64
	failed  atomic.Bool
}

func newRolloverDiagnostics(path string) *rolloverDiagnostics {
	if path == "" {
		return nil
	}
	return &rolloverDiagnostics{started: time.Now(), path: path}
}

func rolloverDiagnosticError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

func (d *rolloverDiagnostics) record(stage string, sequence uint64, pid int64, peer string, changed bool, n int, err error) {
	if d == nil {
		return
	}
	d.add(rolloverTraceEvent{Stage: stage, Sequence: sequence, PID: pid, Peer: peer, PeerChanged: changed, Bytes: n, Error: rolloverDiagnosticError(err)})
}

func (d *rolloverDiagnostics) add(event rolloverTraceEvent) {
	if !d.mu.TryLock() {
		d.dropped.Add(1)
		return
	}
	defer d.mu.Unlock()
	event.ElapsedNS = time.Since(d.started).Nanoseconds()
	d.ring[d.next] = event
	d.next = (d.next + 1) % len(d.ring)
	d.count = min(d.count+1, len(d.ring))
}

// Attach only to the public synthetic freedom upstream, never managed cores.
// exec may call Write before Start returns: PID is an independent atomic set
// AFTER successful Start, not a concurrent read of cmd.Process.
type rolloverUpstreamWriter struct {
	diagnostic *rolloverDiagnostics
	stage      string
	pid        *atomic.Int64
}

func (w *rolloverUpstreamWriter) Write(p []byte) (int, error) {
	if w.diagnostic != nil {
		// Retain the tail: startup errors often follow a banner in one write.
		w.diagnostic.add(rolloverTraceEvent{Stage: w.stage, PID: w.pid.Load(), Bytes: len(p), Output: string(p[max(0, len(p)-512):])})
	}
	// Truncation/contention drops diagnostics, never backpressures the process.
	return len(p), nil
}

func (d *rolloverDiagnostics) snapshot() ([]rolloverTraceEvent, bool) {
	if !d.mu.TryLock() {
		return nil, false
	}
	defer d.mu.Unlock()
	events := make([]rolloverTraceEvent, d.count)
	for i := range events {
		events[i] = d.ring[(d.next-d.count+i+len(d.ring))%len(d.ring)]
	}
	return events, true
}

type rolloverDiagnosticReport struct {
	FailureElapsedNS  int64
	Failure           string
	FailureState      rolloverTraceEvent
	Events            []rolloverTraceEvent
	TraceAvailable    bool
	Dropped           uint64
	Counters          map[string]string
	CounterErrors     map[string]string
	CountersElapsedNS int64
}

// Capture starts BEFORE test cancellation. A blocked diagnostic filesystem may
// delay cancellation at most 200ms; its one best-effort writer can finish later.
// Never return this error in place of the original held-session error.
func (d *rolloverDiagnostics) capture(err error, state rolloverTraceEvent) error {
	if d == nil || !d.failed.CompareAndSwap(false, true) {
		return nil
	}
	events, available := d.snapshot()
	state.ElapsedNS = time.Since(d.started).Nanoseconds()
	state.Error = rolloverDiagnosticError(err)
	report := rolloverDiagnosticReport{FailureElapsedNS: state.ElapsedNS, Failure: state.Error, FailureState: state, Events: events, TraceAvailable: available, Dropped: d.dropped.Load()}
	done := make(chan error, 1)
	go func() {
		report.Counters, report.CounterErrors = rolloverDiagnosticCounters()
		report.CountersElapsedNS = time.Since(d.started).Nanoseconds()
		encoded, encodeErr := json.Marshal(report)
		if encodeErr != nil {
			done <- encodeErr
			return
		}
		if len(encoded) > 256<<10 {
			done <- fmt.Errorf("rollover diagnostics exceed 256KiB")
			return
		}
		// Exclusive create preserves any earlier failed-run evidence. No mkdir,
		// retries, fsync or reuse of an existing path (including FIFO/symlink).
		file, openErr := os.OpenFile(d.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			done <- openErr
			return
		}
		written, writeErr := file.Write(append(encoded, '\n'))
		closeErr := file.Close()
		if writeErr != nil {
			done <- writeErr
		} else if written != len(encoded)+1 {
			done <- io.ErrShortWrite
		} else {
			done <- closeErr
		}
	}()
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case dumpErr := <-done:
		return dumpErr
	case <-timer.C:
		return fmt.Errorf("rollover diagnostic dump exceeded 200ms")
	}
}

func rolloverDiagnosticRead(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err == nil && len(b) > 4096 {
		err = fmt.Errorf("counter exceeds 4096 bytes")
	}
	return string(b[:min(len(b), 4096)]), err
}

func rolloverDiagnosticCounters() (map[string]string, map[string]string) {
	values, failures := map[string]string{}, map[string]string{}
	read := func(key, path string) string {
		s, err := rolloverDiagnosticRead(path)
		if err != nil {
			// Do not copy arbitrary filesystem error strings/paths into reports.
			failures[key] = "unavailable_or_truncated"
		}
		return s
	}
	// Network namespace counters, not per-flow packet attribution.
	snmp := bufio.NewScanner(strings.NewReader(read("udp_snmp", "/proc/net/snmp")))
	for snmp.Scan() {
		if strings.HasPrefix(snmp.Text(), "Udp:") || strings.HasPrefix(snmp.Text(), "UdpLite:") {
			values["udp_snmp"] += snmp.Text() + "\n"
		}
	}
	// Always label namespace-root counters separately: they may include other
	// processes. Also try the exact self cgroup when it lies under the mount.
	dirs := map[string]string{"cgroup_namespace_root": "/sys/fs/cgroup"}
	groups := read("self_cgroup", "/proc/self/cgroup")
	for _, line := range strings.Split(groups, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 || parts[0] != "0" || parts[1] != "" {
			continue
		}
		rel := strings.TrimPrefix(parts[2], "/")
		clean := filepath.Clean(rel)
		if clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator)) && !filepath.IsAbs(clean) {
			dirs["self_cgroup"] = filepath.Join("/sys/fs/cgroup", clean)
		}
	}
	for scope, dir := range dirs {
		for _, name := range []string{"cpu.stat", "cpu.max", "memory.current", "memory.max", "memory.events"} {
			key := scope + "/" + name
			values[key] = read(key, filepath.Join(dir, name))
		}
	}
	return values, failures
}

func TestRolloverDiagnosticsBoundedRingAndDumpFailure(t *testing.T) {
	d := newRolloverDiagnostics(filepath.Join(t.TempDir(), "missing", "failure.json"))
	for i := 0; i < rolloverTraceCapacity+9; i++ {
		d.record("udp_send", uint64(i), 12, "127.0.0.1:3000", false, 24, nil)
	}
	events, ok := d.snapshot()
	if !ok || len(events) != rolloverTraceCapacity || events[0].Sequence != 9 || events[len(events)-1].Sequence != rolloverTraceCapacity+8 {
		t.Fatal("ring did not retain bounded latest events", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].ElapsedNS < events[i-1].ElapsedNS {
			t.Fatal("elapsed time moved backwards")
		}
	}
	d.mu.Lock()
	d.record("contended", 999, 12, "", false, 0, nil)
	_, ok = d.snapshot()
	d.mu.Unlock()
	if ok || d.dropped.Load() != 1 {
		t.Fatal("diagnostics blocked or concealed unavailable snapshot")
	}
	if err := d.capture(fmt.Errorf("original held failure"), rolloverTraceEvent{}); err == nil {
		t.Fatal("missing-parent dump unexpectedly succeeded")
	}
}

func TestRolloverUpstreamWriterBoundedAndNonblocking(t *testing.T) {
	d := newRolloverDiagnostics(filepath.Join(t.TempDir(), "unused.json"))
	var pid atomic.Int64
	w := &rolloverUpstreamWriter{diagnostic: d, stage: "upstream_stderr", pid: &pid}
	payload := []byte(strings.Repeat("public upstream output ", 200))
	d.mu.Lock()
	n, err := w.Write(payload) // No Process access before Start/PID publication.
	d.mu.Unlock()
	if n != len(payload) || err != nil || d.dropped.Load() != 1 {
		t.Fatal("contended writer backpressured synthetic upstream", n, err)
	}
	pid.Store(123)
	n, err = w.Write(payload)
	events, ok := d.snapshot()
	if n != len(payload) || err != nil || !ok || len(events) != 1 {
		t.Fatal("missing synthetic output", n, err, len(events))
	}
	if events[0].PID != 123 || events[0].Stage != "upstream_stderr" || events[0].Output != string(payload[len(payload)-512:]) || events[0].Bytes != len(payload) {
		t.Fatal("unbounded or incorrectly attributed output", events[0])
	}
}
