package topology

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

// Offer never waits for UI, serialization, a lock, DNS or a network peer.
// Fixed queues bound memory even when control plane is unreachable.
type Observer struct {
	Epoch    string
	normal   chan Decision
	priority chan Decision
	seen     atomic.Uint64
	dropped  atomic.Uint64
	sampled  atomic.Uint64
	sequence atomic.Uint64
	flows    atomic.Uint64
	every    uint64
}

func NewObserver() *Observer {
	if os.Getenv("TOPOLOGY_ENABLED") == "false" {
		return nil
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return &Observer{Epoch: hex.EncodeToString(b[:]), normal: make(chan Decision, boundedEnv("TOPOLOGY_QUEUE_SIZE", 128, 16, 256)), priority: make(chan Decision, boundedEnv("TOPOLOGY_QUEUE_SIZE", 128, 16, 256)), every: uint64(boundedEnv("TOPOLOGY_SAMPLE_EVERY", 16, 1, 1024))}
}
func (o *Observer) Begin() bool {
	if o == nil {
		return false
	}
	n := o.seen.Add(1)
	if (n-1)%o.every != 0 {
		o.sampled.Add(1)
		return false
	}
	return true
}
func (o *Observer) FlowID() string {
	if o == nil {
		return ""
	}
	return o.Epoch + "-flow-" + itoa(o.flows.Add(1))
}
func (o *Observer) Offer(d Decision) {
	if o == nil {
		return
	}
	d.FinishedAt = time.Now().UTC()
	if d.At.IsZero() {
		d.At = d.FinishedAt
	}
	d.ID = o.Epoch + "-" + itoa(o.sequence.Add(1))
	q := o.normal
	if d.Reason != "selected_by_policy" {
		q = o.priority
	}
	select {
	case q <- d:
	default:
		o.dropped.Add(1)
	}
}
func itoa(n uint64) string {
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte(n%10) + '0'
		n /= 10
		if n == 0 {
			return string(b[i:])
		}
	}
}
func (o *Observer) Fill(f *Frame) {
	if o == nil {
		return
	}
	f.Epoch = o.Epoch
	f.Dropped = o.dropped.Load()
	f.Sampled = o.sampled.Load()
	f.SampleEvery = int(o.every)
	f.Decisions = make([]Decision, 0, 256)
	for _, q := range []chan Decision{o.priority, o.normal} {
		for len(f.Decisions) < 256 {
			select {
			case d := <-q:
				f.Decisions = append(f.Decisions, d)
			default:
				goto next
			}
		}
	next:
	}
}
func boundedEnv(key string, fallback, low, high int) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return min(high, max(low, n))
}
func (o *Observer) Metrics(w io.Writer) {
	if o == nil {
		return
	}
	fmt.Fprintf(w, "relayweave_topology_dropped_events_total %d\nrelayweave_topology_sampled_out_total %d\nrelayweave_topology_buffer_fill %d\nrelayweave_topology_sampling_ratio %g\n", o.dropped.Load(), o.sampled.Load(), len(o.normal)+len(o.priority), 1/float64(o.every))
}
