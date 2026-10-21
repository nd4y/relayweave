package worker

import (
	"context"
	"io"
	"net"
	"os"
	"proxyplatform/internal/config"
	"proxyplatform/internal/socks"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// An opt-in socket benchmark of the real gateway/admission/scorer, with shared
// per-lane byte pacing. It complements, rather than replaces, native core tests.
// No Internet, router, privileged qdisc or production configuration is required.
func TestAutomaticShapedSocketPerformance(t *testing.T) {
	if os.Getenv("ROLLOVER_PERFORMANCE_TEST") != "1" {
		t.Skip("shaped socket acceptance opt-in")
	}
	type result struct {
		bps     float64
		latency []time.Duration
		errors  int64
	}
	run := func(mode string, share float64, mixed bool) result {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		r := selectionFixture("auto")
		r.ctx = ctx
		r.config = config.Config{MaxConnections: 100, ConnectTimeoutMS: 1000}
		g := New(ctx, r.config, nil)
		g.Dynamic = r
		defer g.Close()
		var servers sync.WaitGroup
		var accepting sync.WaitGroup
		var listeners []net.Listener
		for index, id := range []string{"a", "b"} {
			bandwidth := int64(4 << 20)
			if index == 1 {
				bandwidth = 1 << 20
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			listeners = append(listeners, listener)
			v := r.current[id].route
			v.Config.Endpoint = listener.Addr().String()
			v.Config.ServerHost = []string{"1.1.1.1", "8.8.8.8"}[index]
			r.entries[id] = r.current[id]
			// Shared lane, not a separate bandwidth allowance per connection.
			var pace sync.Mutex
			next := time.Now()
			accepting.Add(1)
			go func() {
				defer accepting.Done()
				for {
					c, e := listener.Accept()
					if e != nil {
						return
					}
					servers.Add(1)
					go func() {
						defer servers.Done()
						defer c.Close()
						c.SetDeadline(time.Now().Add(5 * time.Second))
						if _, e := io.ReadFull(c, make([]byte, 3)); e != nil {
							return
						}
						if e := socks.Write(c, []byte{5, 0}); e != nil {
							return
						}
						if _, e := io.ReadFull(c, make([]byte, 3)); e != nil {
							return
						}
						if _, _, _, e := socks.ReadAddress(c); e != nil {
							return
						}
						if e := socks.Reply(c, 0, "0.0.0.0", 0); e != nil {
							return
						}
						kind := []byte{0}
						if _, e := io.ReadFull(c, kind); e != nil {
							return
						}
						size := 256 << 10
						if kind[0] == 1 {
							size = 1
						}
						time.Sleep(5 * time.Millisecond)
						payload := make([]byte, 16<<10)
						for size > 0 {
							n := min(size, len(payload))
							pace.Lock()
							next = maxTime(next, time.Now()).Add(time.Duration(float64(n) / float64(bandwidth) * float64(time.Second)))
							at := next
							pace.Unlock()
							select {
							case <-ctx.Done():
								return
							case <-time.After(time.Until(at)):
							}
							if e := socks.Write(c, payload[:n]); e != nil {
								return
							}
							size -= n
						}
					}()
				}
			}()
		}
		// Learn useful rates from transferred payload, without handing the scorer
		// the shaper's configured rate or the best static allocation.
		for _, id := range []string{"a", "b"} {
			v := r.current[id].route
			started := time.Now()
			c, err := v.Instance.Adapter().OpenTCP(ctx, v.Config.Endpoint, "origin.test:443", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			c.SetDeadline(time.Now().Add(time.Second))
			if _, err = c.Write([]byte{0}); err != nil {
				c.Close()
				t.Fatal(err)
			}
			n, err := io.CopyN(io.Discard, c, 256<<10)
			c.Close()
			if err != nil {
				t.Fatal(err)
			}
			v.SpeedBPS.Store(int64(float64(n) / time.Since(started).Seconds()))
			v.LastSpeed.Store(time.Now().Unix())
		}
		if mode == "static" {
			p := r.policies["p"]
			p.autoWeights = map[*Route]float64{r.current["a"].route: share, r.current["b"].route: 1 - share}
			p.autoAt = time.Now().Add(time.Hour)
		}
		var bytes, failures atomic.Int64
		var jobs sync.WaitGroup
		var mu sync.Mutex
		latency := []time.Duration{}
		deadline, _ := ctx.Deadline()
		unexpected := func() bool { return ctx.Err() == nil && time.Until(deadline) > 20*time.Millisecond }
		request := func(interactive bool) {
			started := time.Now()
			c, _, e := g.dial(ctx, "policy:p", "origin.test:443")
			if e != nil {
				if unexpected() {
					failures.Add(1)
				}
				return
			}
			defer c.Close()
			c.SetDeadline(deadline)
			kind, size := byte(0), int64(256<<10)
			if interactive {
				kind, size = 1, 1
			}
			if _, e = c.Write([]byte{kind}); e != nil {
				if unexpected() {
					failures.Add(1)
				}
				return
			}
			n, e := io.CopyN(io.Discard, c, size)
			if !interactive {
				bytes.Add(n)
			}
			if e != nil {
				if unexpected() {
					failures.Add(1)
				}
				return
			}
			if interactive {
				mu.Lock()
				latency = append(latency, time.Since(started))
				mu.Unlock()
			}
		}
		started := time.Now()
		for range 8 {
			jobs.Add(1)
			go func() {
				defer jobs.Done()
				for ctx.Err() == nil {
					request(false)
				}
			}()
		}
		if mixed {
			jobs.Add(1)
			go func() {
				defer jobs.Done()
				var interactive sync.WaitGroup
				defer interactive.Wait()
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						interactive.Add(1)
						go func() { defer interactive.Done(); request(true) }()
					}
				}
			}()
		}
		jobs.Wait()
		cancel()
		for _, listener := range listeners {
			listener.Close()
		}
		accepting.Wait()
		servers.Wait()
		sort.Slice(latency, func(i, j int) bool { return latency[i] < latency[j] })
		return result{float64(bytes.Load()) / time.Since(started).Seconds(), latency, failures.Load()}
	}
	percentile := func(values []time.Duration, p float64) time.Duration {
		if len(values) == 0 {
			return 0
		}
		return values[min(len(values)-1, int(float64(len(values))*p))]
	}
	for _, mixed := range []bool{false, true} {
		static := map[float64][]result{}
		automatic := []result{}
		for repeat := 0; repeat < 5; repeat++ {
			for _, share := range []float64{0, .5, .8, 1} {
				static[share] = append(static[share], run("static", share, mixed))
			}
			automatic = append(automatic, run("automatic", 0, mixed))
		}
		average := func(values []result) result {
			out := result{}
			for _, v := range values {
				out.bps += v.bps / float64(len(values))
				out.latency = append(out.latency, v.latency...)
				out.errors += v.errors
			}
			sort.Slice(out.latency, func(i, j int) bool { return out.latency[i] < out.latency[j] })
			return out
		}
		best := result{}
		for share, values := range static {
			v := average(values)
			t.Logf("mixed=%v static_share=%.2f goodput=%.3fMiB/s p50=%s p95=%s p99=%s errors=%d", mixed, share, v.bps/(1<<20), percentile(v.latency, .5), percentile(v.latency, .95), percentile(v.latency, .99), v.errors)
			if v.bps > best.bps {
				best = v
			}
		}
		auto := average(automatic)
		t.Logf("mixed=%v automatic goodput=%.3fMiB/s ratio=%.4f p50=%s p95=%s p99=%s errors=%d", mixed, auto.bps/(1<<20), auto.bps/best.bps, percentile(auto.latency, .5), percentile(auto.latency, .95), percentile(auto.latency, .99), auto.errors)
		gate := .9
		if mixed {
			gate = .85
			baseline := percentile(best.latency, .95)
			if percentile(auto.latency, .95) > baseline+max(baseline/5, 20*time.Millisecond) {
				t.Error("mixed interactive latency regression")
			}
		}
		if auto.bps < gate*best.bps || auto.errors != 0 {
			t.Error("automatic socket performance gate failed")
		}
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
