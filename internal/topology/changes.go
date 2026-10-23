package topology

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const ChangeLimit = 64

func (h *Hub) changes(old Frame, f Frame, exists bool) []Change {
	out := []Change{}
	add := func(kind string, r Route, before, after string) {
		if len(out) >= ChangeLimit {
			return
		}
		clip := func(s string) string {
			if len(s) > 192 {
				return s[:189] + "..."
			}
			return s
		}
		out = append(out, Change{ID: fmt.Sprintf("%s-change-%d-%d", h.epoch, h.seq+1, len(out)), Worker: f.Worker, Epoch: f.Epoch, Kind: kind, Route: r.ID, Engine: r.Engine, Before: clip(before), After: clip(after), At: f.At, PreviousAt: old.At, Generation: f.Generation, Source: "observed_worker_frame_difference"})
	}
	if !exists {
		add("worker_observed", Route{}, "unknown", f.Epoch)
	} else if old.Epoch != f.Epoch {
		add("worker_epoch_changed", Route{}, old.Epoch, f.Epoch)
	}
	if exists && old.Generation != f.Generation {
		add("configuration_observed", Route{}, strconv.FormatInt(old.Generation, 10), strconv.FormatInt(f.Generation, 10))
	}
	prior := map[string]Route{}
	for _, r := range old.Routes {
		prior[r.Engine] = r
	}
	for _, r := range f.Routes {
		p, ok := prior[r.Engine]
		if !ok {
			add("engine_observed", r, "unknown", r.State)
			continue
		}
		if p.State != r.State {
			add("core_state_observed", r, p.State, r.State)
		}
		if strings.Join(p.ServerIPs, ",") != strings.Join(r.ServerIPs, ",") {
			add("dns_observed", r, strings.Join(p.ServerIPs, ","), strings.Join(r.ServerIPs, ","))
		}
		if p.Group != r.Group {
			add("group_observed", r, p.Group, r.Group)
		}
		delete(prior, r.Engine)
	}
	for _, r := range prior {
		add("engine_no_longer_observed", r, r.State, "unknown; not evidence of tunnel termination")
	}
	previous := []Change{}
	for _, c := range old.Changes {
		if time.Since(c.At) <= h.decisionTTL {
			previous = append(previous, c)
		}
	}
	out = append(previous, out...)
	if len(out) > ChangeLimit {
		out = append([]Change(nil), out[len(out)-ChangeLimit:]...)
	}
	return out
}
func (h *Hub) trimChanges(list []Change, now time.Time) []Change {
	expired := false
	for _, c := range list {
		if now.Sub(c.At) > h.decisionTTL {
			expired = true
			break
		}
	}
	if !expired {
		return list
	}
	out := []Change{}
	for _, c := range list {
		if now.Sub(c.At) <= h.decisionTTL {
			out = append(out, c)
		}
	}
	return out
}
