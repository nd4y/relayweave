package worker

import (
	"context"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"proxyplatform/internal/config"
	"proxyplatform/internal/routing"
	"proxyplatform/internal/topology"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func local16Fixture() *Runtime {
	r := selectionFixture("auto")
	r.localV1 = true
	p := r.policies["p"]
	p.best = nil
	p.config.ActivePaths = 16
	p.config.Routes = nil
	r.config.Routes = nil
	for i := 0; i < 256; i++ {
		id := strconv.Itoa(i)
		p.config.Routes = append(p.config.Routes, id)
		r.config.Routes = append(r.config.Routes, config.Route{ID: id, CapacityGroup: id})
	}
	for i := 0; i < 16; i++ {
		id := strconv.Itoa(i)
		v := readyRoute(id)
		v.Active.Store(1)
		r.current[id] = &engineEntry{route: v}
		p.best = append(p.best, v)
	}
	r.prepareLocalLocked(p, p.best, time.Now(), false)
	return r
}

func BenchmarkLocalV1Runtime16(b *testing.B) {
	r := local16Fixture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Candidates("policy:p")
	}
}

// Explicit opt-in: run only on build-01 without race instrumentation. Includes
// the actual runtime selector and background preparation, not just Plan.Pick.
func TestLocalV1ResourceEnvelope(t *testing.T) {
	if os.Getenv("RW_RESOURCE_GATE") != "1" {
		t.Skip("explicit paired build-host resource gate")
	}
	rss := func() int64 {
		raw, e := os.ReadFile("/proc/self/statm")
		if e != nil {
			t.Fatal(e)
		}
		fields := strings.Fields(string(raw))
		n, e := strconv.ParseInt(fields[1], 10, 64)
		if e != nil {
			t.Fatal(e)
		}
		return n * int64(os.Getpagesize())
	}
	seconds := func(v unix.Timeval) float64 { return float64(v.Sec) + float64(v.Usec)/1e6 }
	usage := func() float64 {
		var u unix.Rusage
		if e := unix.Getrusage(unix.RUSAGE_SELF, &u); e != nil {
			t.Fatal(e)
		}
		return seconds(u.Utime) + seconds(u.Stime)
	}
	baseline := local16Fixture()
	baseline.localV1 = false
	for _, v := range baseline.policies["p"].best {
		v.localEnabled.Store(false)
	}
	for range 1000 {
		baseline.Candidates("policy:p")
	}
	runtime.GC()
	baseRSS := rss()
	local := local16Fixture()
	for _, v := range local.policies["p"].best {
		v.localEnabled.Store(true)
	}
	for range 1000 {
		local.Candidates("policy:p")
	}
	runtime.GC()
	peak := rss()
	var cpu, wall [2]float64
	var connections [2]int
	var latency [2][]int64
	for i := range latency {
		latency[i] = make([]int64, 0, 100000)
	}
	// Twenty randomized-order paired 30-second blocks give ten minutes per
	// condition. Both arms include the same timer/RSS harness and runtime calls.
	for pair := 0; pair < 20; pair++ {
		order := [2]int{pair % 2, 1 - pair%2}
		for _, arm := range order {
			r := baseline
			if arm == 1 {
				r = local
			}
			ctx, cancel := context.WithCancel(context.Background())
			r.ctx = ctx
			done := make(chan struct{})
			if arm == 1 {
				go func() { defer close(done); r.localBackground() }()
			} else {
				close(done)
			}
			before := usage()
			start := time.Now()
			end := start.Add(30 * time.Second)
			count := 0
			nextRSS := start.Add(time.Second)
			tick := time.NewTicker(10 * time.Millisecond)
			for now := range tick.C {
				target := int(now.Sub(start).Seconds() * 1000)
				for n := 0; n < 100 && count < target; n++ {
					started := time.Now()
					routes := r.Candidates("policy:p")
					if len(latency[arm]) < cap(latency[arm]) {
						latency[arm] = append(latency[arm], time.Since(started).Nanoseconds())
					}
					if len(routes) > 0 {
						routes[0].success(10 * time.Millisecond)
					}
					count++
				}
				if !now.Before(nextRSS) {
					peak = max(peak, rss())
					nextRSS = now.Add(time.Second)
				}
				if !now.Before(end) {
					break
				}
			}
			tick.Stop()
			cancel()
			<-done
			cpu[arm] += usage() - before
			wall[arm] += time.Since(start).Seconds()
			connections[arm] += count
		}
	}
	deltaCPU := max(0, cpu[1]/wall[1]-cpu[0]/wall[0])
	var p99 [2]int64
	for i := range latency {
		sort.Slice(latency[i], func(a, b int) bool { return latency[i][a] < latency[i][b] })
		if len(latency[i]) > 0 {
			p99[i] = latency[i][len(latency[i])*99/100]
		}
	}
	deltaP99 := max(int64(0), p99[1]-p99[0])
	t.Logf("selection_p99_baseline_ns=%d local_ns=%d extra_ns=%d", p99[0], p99[1], deltaP99)
	extraRSS := max(int64(0), peak-baseRSS)
	result, _ := json.Marshal(map[string]any{"baseline_cpu_seconds": cpu[0], "local_cpu_seconds": cpu[1], "baseline_wall_seconds": wall[0], "local_wall_seconds": wall[1], "extra_cpu_fraction": deltaCPU, "extra_rss_bytes": extraRSS, "baseline_connections": connections[0], "local_connections": connections[1], "catalogue_routes": 256, "prepared_routes": 16})
	t.Log(string(result))
	if deltaCPU > .02 || extraRSS > 32<<20 || deltaP99 > 100000 || float64(connections[0])/wall[0] < 990 || float64(connections[1])/wall[1] < 990 {
		t.Fatal("paired resource envelope exceeded or load not sustained")
	}
}
func TestLocalV1AutonomousAndImmediateFences(t *testing.T) {
	r := selectionFixture("auto")
	r.localV1 = true
	p := r.policies["p"]
	r.mu.Lock()
	r.prepareLocalLocked(p, p.best, time.Now(), false)
	r.mu.Unlock()
	d := &topology.Decision{}
	if got := r.candidatesObserved("policy:p", d); len(got) != 2 || d.Method != "local_v1_weighted" {
		t.Fatal("no standalone plan", d)
	}
	r.current["a"].route.Instance.Ready.Store(false)
	if got := r.Candidates("policy:p"); len(got) != 1 || got[0].Config.ID != "b" {
		t.Fatal("stale health bypass")
	}
	r.RenewLease(time.Now().Add(-time.Minute))
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("security lease bypass")
	}
}

func TestLocalV1ProbeBudgetSharedAcrossPoliciesAndRestart(t *testing.T) {
	r := selectionFixture("auto")
	r.localV1 = true
	r.config.Policies = []config.Policy{{ID: "a", ProbeBudgetBytes: 128 << 20}, {ID: "b", ProbeBudgetBytes: 128 << 20}}
	now := time.Now().UTC()
	for i := 0; i < 8; i++ {
		key := []string{"a", "b"}[i%2]
		if !r.reserveProbe(now, key, 8<<20, 128<<20) {
			t.Fatal("premature probe rejection", i)
		}
	}
	if r.reserveProbe(now, "new-policy", 8<<20, 128<<20) {
		t.Fatal("probe allowance multiplied by policies")
	}
	restarted := selectionFixture("auto")
	restarted.localV1 = true
	restarted.probeWindow = r.probeWindow
	restarted.budget = r.budget
	if restarted.reserveProbe(now, "after-restart", 8<<20, 128<<20) {
		t.Fatal("probe restart replenished allowance")
	}
	if restarted.reserveProbe(now.Add(-24*time.Hour), "clock-rollback", 8<<20, 128<<20) {
		t.Fatal("probe rollback replenished allowance")
	}
}

// Absolute idle process CPU is a conservative upper bound on the increment;
// this avoids subtracting a noisy near-zero baseline on a shared build host.
func TestLocalV1IdleEnvelope(t *testing.T) {
	if os.Getenv("RW_RESOURCE_IDLE_GATE") != "1" {
		t.Skip("explicit ten-minute build-host idle gate")
	}
	rss := func() int64 {
		raw, e := os.ReadFile("/proc/self/statm")
		if e != nil {
			t.Fatal(e)
		}
		fields := strings.Fields(string(raw))
		n, e := strconv.ParseInt(fields[1], 10, 64)
		if e != nil {
			t.Fatal(e)
		}
		return n * int64(os.Getpagesize())
	}
	cpu := func() float64 {
		var u unix.Rusage
		if e := unix.Getrusage(unix.RUSAGE_SELF, &u); e != nil {
			t.Fatal(e)
		}
		return float64(u.Utime.Sec+u.Stime.Sec) + float64(u.Utime.Usec+u.Stime.Usec)/1e6
	}
	runtime.GC()
	beforeRSS := rss()
	r := local16Fixture()
	for _, v := range r.policies["p"].best {
		v.Active.Store(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	r.ctx = ctx
	done := make(chan struct{})
	go func() { defer close(done); r.localBackground() }()
	defer func() { cancel(); <-done }()
	start := time.Now()
	beforeCPU := cpu()
	peak := rss()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-tick.C:
			peak = max(peak, rss())
		}
	}
	usedCPU := cpu() - beforeCPU
	wall := time.Since(start).Seconds()
	extraRSS := max(int64(0), peak-beforeRSS)
	t.Logf("idle_cpu_seconds=%g idle_wall_seconds=%g idle_cpu_fraction=%g extra_rss_bytes=%d", usedCPU, wall, usedCPU/wall, extraRSS)
	if usedCPU/wall > .005 || extraRSS > 16<<20 {
		t.Fatal("idle resource envelope exceeded")
	}
}

func TestLocalV1DisabledSingleISPAndRetries(t *testing.T) {
	r := selectionFixture("auto")
	r.localV1 = true
	r.config.EgressPaths = []config.EgressPath{{ID: "isp1", Enabled: false, Weight: 8}, {ID: "isp2", Enabled: true, Weight: 5}}
	r.current["a"].route.Config.EgressID = "isp1"
	r.current["b"].route.Config.EgressID = "isp2"
	for range 10 {
		got := r.Candidates("policy:p")
		if len(got) != 1 || got[0].Config.EgressID != "isp2" {
			t.Fatal("disabled ISP in retry list")
		}
	}
	r.current["b"].route.Instance.Ready.Store(false)
	if len(r.Candidates("policy:p")) != 0 {
		t.Fatal("disabled lone ISP selected")
	}
}

func TestLocalV1ManualUnchangedAndUDPNoTCPGoodput(t *testing.T) {
	r := selectionFixture("manual")
	r.localV1 = true
	if r.Candidates("policy:p")[0].Config.ID != "a" {
		t.Fatal("manual changed")
	}
	p := r.policies["p"]
	p.config.Mode = "auto"
	for _, v := range p.best {
		v.Config.UDP = true
	}
	a := r.current["a"].route
	a.SpeedBPS.Store(1_000_000_000)
	a.LastSpeed.Store(time.Now().Unix())
	r.prepareLocalLocked(p, p.best, time.Now(), true)
	if p.localPlans[1].Shares[0].PPM != 500000 {
		t.Fatal("UDP used TCP speed")
	}
	// A cached plan cannot bypass a subsequent source/route restriction.
	r.Restrict(config.Config{Routes: []config.Route{r.current["b"].route.Config}, Policies: []config.Policy{{ID: "p", Routes: []string{"b"}}}})
	if got := r.candidatesForProtocol("policy:p", nil, true); len(got) != 1 || got[0].Config.ID != "b" {
		t.Fatal("cached revocation bypass")
	}
}

func BenchmarkLocalV1RuntimeSelection(b *testing.B) {
	r := selectionFixture("auto")
	r.localV1 = true
	p := r.policies["p"]
	p.localPlans[0] = routing.Build([]routing.Input{{ID: "a", RTTMS: 100}, {ID: "b", RTTMS: 100}}, nil)
	p.localAt[0] = time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Candidates("policy:p")
	}
}
