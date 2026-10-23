package worker

import (
	"sync"
	"testing"
	"time"
)

func TestLowDeliveredRateRequestsConfirmationWithoutChangingCapacity(t *testing.T) {
	now := time.Now()
	for _, test := range []struct {
		name    string
		window  uint32
		active  int64
		age     time.Duration
		backoff bool
		request bool
	}{
		{"missing receive-window field", 0, 4, time.Minute, false, true},
		{"receiver has room", 1 << 20, 4, time.Minute, false, true},
		{"known client backpressure", 32768, 4, time.Minute, false, false},
		{"ordinary parallel sharing", 0, 80, time.Minute, false, false},
		{"recent independent sample", 0, 4, 10 * time.Second, false, false},
		{"unchanged confirmation cooldown", 0, 4, time.Minute, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := readyRoute("path")
			r.SpeedBPS.Store(25_000_000)
			r.LastSpeed.Store(now.Add(-test.age).Unix())
			r.Active.Store(test.active)
			if test.backoff {
				r.ProbeNotBefore.Store(now.Add(time.Minute).Unix())
			}
			before := r.score("throughput")
			r.recordDeliveredRate(500_000, test.window, now)
			if (r.ProbeRequestedAt.Load() != 0) != test.request {
				t.Fatalf("independent confirmation requested=%v, want %v", r.ProbeRequestedAt.Load() != 0, test.request)
			}
			if r.SpeedBPS.Load() != 25_000_000 || r.score("throughput") != before || !r.Instance.Ready.Load() || r.BlockedUntil.Load() != 0 {
				t.Fatal("a passive hint changed confirmed capacity, rank or availability")
			}
			if r.PassiveBPS.Load() != 500_000 || r.LastPassive.Load() != now.Unix() {
				t.Fatal("delivered lower bound was lost")
			}
		})
	}
}

func TestCapacityDropRevalidatesOnlyEligibleOlderAlternatives(t *testing.T) {
	r := selectionFixture("throughput")
	a, b := r.current["a"].route, r.current["b"].route
	epoch := time.Now().Unix()
	a.SpeedBPS.Store(2_500_000)
	a.LastSpeed.Store(epoch + 2)
	b.LastSpeed.Store(epoch - 5)
	b.ProbeNotBefore.Store(epoch + 300)
	r.capacityConfirmed("p", a, 25_000_000, epoch)
	if b.ProbeRequestedAt.Load() != epoch || b.ProbeNotBefore.Load() != 0 || a.ProbeRequestedAt.Load() != 0 {
		t.Fatal("confirmed drop did not revalidate the older alternative")
	}
	// Propagate the original observation epoch, rather than starting another
	// round when a second member of the same cohort also confirms a drop.
	b.SpeedBPS.Store(250_000)
	b.LastSpeed.Store(epoch + 12)
	b.ProbeRequestedAt.Store(0)
	r.capacityConfirmed("p", b, 2_500_000, epoch)
	if a.ProbeRequestedAt.Load() != 0 {
		t.Fatal("a refreshed member was scheduled again by the same probe round")
	}
	r.capacityConfirmed("p", a, 25_000_000, epoch)
	if b.ProbeRequestedAt.Load() != 0 {
		t.Fatal("an alternative measured after the observation was probed again")
	}
	b.LastSpeed.Store(epoch - 5)
	r.valid["b"] = time.Now().Add(-time.Second)
	r.capacityConfirmed("p", a, 25_000_000, epoch)
	if b.ProbeRequestedAt.Load() != 0 {
		t.Fatal("expired alternative received a new capacity probe")
	}
}

func TestUnchangedCapacityConfirmationBacksOffPassiveHints(t *testing.T) {
	r := selectionFixture("throughput")
	a := r.current["a"].route
	now := time.Now()
	a.SpeedBPS.Store(24_000_000)
	a.LastSpeed.Store(now.Unix())
	a.Active.Store(4)
	r.capacityConfirmed("p", a, 25_000_000, now.Add(-time.Minute).Unix())
	a.recordDeliveredRate(500_000, 0, now.Add(time.Minute))
	if a.ProbeRequestedAt.Load() != 0 {
		t.Fatal("a slow consumer immediately spent another confirmation sample")
	}
	a.recordDeliveredRate(500_000, 0, now.Add(301*time.Second))
	if a.ProbeRequestedAt.Load() == 0 {
		t.Fatal("confirmation cooldown suppressed future observations indefinitely")
	}
}

func TestCapacityProbeCoalescesTheEarliestConcurrentObservation(t *testing.T) {
	r := readyRoute("path")
	var done sync.WaitGroup
	for epoch := int64(100); epoch < 164; epoch++ {
		done.Add(1)
		go func(value int64) { defer done.Done(); r.requestCapacityProbe(value) }(epoch)
	}
	done.Wait()
	if r.ProbeRequestedAt.Load() != 100 {
		t.Fatal("concurrent hints lost the original change epoch")
	}
}
