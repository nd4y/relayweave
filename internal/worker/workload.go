package worker

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// Observations never grant admission or change independently checked capacity.
// The sampler owns the history; selectors read bounded atomic snapshots only.
type deliveryWindow struct {
	mu                         sync.Mutex
	at                         time.Time
	down10, up10, down60, up60 float64
	Down10, Up10, Down60, Up60 atomic.Int64
}

func (w *deliveryWindow) observe(down, up int64, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	seconds := now.Sub(w.at).Seconds()
	if w.at.IsZero() || seconds <= 0 || seconds > 3 {
		w.down10, w.down60 = float64(down), float64(down)
		w.up10, w.up60 = float64(up), float64(up)
	} else {
		short, long := -math.Expm1(-seconds/10), -math.Expm1(-seconds/60)
		w.down10 += short * (float64(down) - w.down10)
		w.up10 += short * (float64(up) - w.up10)
		w.down60 += long * (float64(down) - w.down60)
		w.up60 += long * (float64(up) - w.up60)
	}
	w.at = now
	w.Down10.Store(int64(w.down10))
	w.Up10.Store(int64(w.up10))
	w.Down60.Store(int64(w.down60))
	w.Up60.Store(int64(w.up60))
}

// Unobserved/new TCP connections and UDP associations retain full weight.
// Only an individually sampled, non-transferring TCP socket is discounted.
// A small idle cost preserves headroom for a tunnel that resumes transferring.
func (r *Route) workload(now time.Time) float64 {
	active := max(int64(0), r.Active.Load())
	at := r.WorkloadAt.Load()
	if age := now.Unix() - at; at == 0 || age < 0 || age > 3 {
		return float64(active)
	}
	idle := min(active, max(int64(0), r.IdleTCP.Load()))
	return float64(active-idle) + float64(idle)*.05
}
