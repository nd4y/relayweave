package worker

import "time"

const capacityRefreshSeconds = 300

// Preserve the first observation of a possible change across a probe round.
// Alternatives already measured after that observation need no repeat probe.
func (r *Route) requestCapacityProbe(since int64) {
	for {
		old := r.ProbeRequestedAt.Load()
		if old != 0 && old <= since {
			return
		}
		if r.ProbeRequestedAt.CompareAndSwap(old, since) {
			return
		}
	}
}

func (r *Route) recordDeliveredRate(bps int64, receiveWindow uint32, now time.Time) {
	r.PassiveBPS.Store(bps)
	r.LastPassive.Store(now.Unix())
	capacity := r.SpeedBPS.Load()
	expected := capacity / max(int64(1), r.Active.Load())
	improved := capacity > 0 && bps > capacity+capacity/2
	// Older TCP_INFO layouts omit rcv_wnd and leave zero here. A low delivered
	// rate then requests independent confirmation, never a capacity downgrade.
	// A known small receive window still protects client backpressure.
	degraded := expected > 0 && bps < expected/4 && (receiveWindow == 0 || receiveWindow >= 65536)
	if (improved || degraded) && now.Unix()-r.LastSpeed.Load() > 30 && now.Unix() >= r.ProbeNotBefore.Load() {
		r.requestCapacityProbe(now.Unix())
	}
}

func (r *Runtime) capacityConfirmed(policyID string, route *Route, previous, since int64) {
	current := route.SpeedBPS.Load()
	if previous <= 0 || current <= 0 {
		return
	}
	if current < previous/2 {
		for _, other := range r.Candidates("policy:" + policyID) {
			if other == route || other.LastSpeed.Load() >= since {
				continue
			}
			// A confirmed change is stronger evidence than an earlier negative
			// passive hint. Revalidate eligible alternatives within the same
			// bounded, serialized and durably reserved probe mechanism.
			other.ProbeNotBefore.Store(0)
			other.requestCapacityProbe(since)
		}
	} else if current <= previous+previous/2 {
		// A slow destination/consumer can also cause low delivered goodput.
		// Successful unchanged confirmation keeps the normal five-minute
		// refresh cadence rather than spending the budget every thirty seconds.
		route.ProbeNotBefore.Store(route.LastSpeed.Load() + capacityRefreshSeconds)
	}
}
