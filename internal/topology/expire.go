package topology

import "time"

// Also called by existing control-plane maintenance while no workers/UI exist.
func (h *Hub) Expire() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.expireLocked(time.Now())
}
func (h *Hub) expireLocked(now time.Time) {
	for id, d := range h.decisions {
		if now.Sub(d.At) > h.decisionTTL {
			delete(h.decisions, id)
		}
	}
	order := make([]string, 0, len(h.order))
	for _, id := range h.order {
		if _, ok := h.decisions[id]; ok {
			order = append(order, id)
		}
	}
	h.order = order
	trim := func(list []Decision) []Decision {
		expired := false
		for _, d := range list {
			if now.Sub(d.At) > h.decisionTTL {
				expired = true
				break
			}
		}
		if !expired {
			return list
		}
		out := make([]Decision, 0, len(list))
		for _, d := range list {
			if now.Sub(d.At) <= h.decisionTTL {
				out = append(out, d)
			}
		}
		return out
	}
	for id, f := range h.workers {
		f.Decisions = trim(f.Decisions)
		f.Changes = h.trimChanges(f.Changes, now)
		h.workers[id] = f
	}
	for i, e := range h.events {
		e.Frame.Decisions = trim(e.Frame.Decisions)
		e.Frame.Changes = h.trimChanges(e.Frame.Changes, now)
		h.events[i] = e
	}
	history := make([]Summary, 0, len(h.history))
	for _, s := range h.history {
		if now.Sub(s.At) <= time.Hour {
			history = append(history, s)
		}
	}
	h.history = history
}
