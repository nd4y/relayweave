package topology

import (
	"testing"
	"time"
)

func TestObservedChangesAreFrameFactsNotSelectorCauses(t *testing.T) {
	h := NewHub()
	first := frame(1)
	first.Generation = 1
	first.Routes[0].Engine = "engine"
	first.Routes[0].State = "ready"
	first.Routes[0].ServerIPs = []string{"192.0.2.1"}
	h.Accept("worker-1", first)
	next := frame(2)
	next.Generation = 2
	next.Routes[0].Engine = "engine"
	next.Routes[0].State = "core_not_ready"
	next.Routes[0].ServerIPs = []string{"192.0.2.2"}
	h.Accept("worker-1", next)
	seen := map[string]bool{}
	for _, c := range h.Snapshot().Workers[0].Changes {
		seen[c.Kind] = true
		if c.Source != "observed_worker_frame_difference" || c.Worker != "worker-1" {
			t.Fatal("provenance")
		}
	}
	if !seen["configuration_observed"] || !seen["core_state_observed"] || !seen["dns_observed"] {
		t.Fatal(seen)
	}
	h.decisionTTL = time.Nanosecond
	h.Expire()
	if len(h.Snapshot().Workers[0].Changes) != 0 {
		t.Fatal("expired changes retained")
	}
}
