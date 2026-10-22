package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"proxyplatform/internal/config"
)

const capacityHintLifetime = 15 * time.Minute

type capacityHint struct {
	BPS int64 `json:"bps"`
	At  int64 `json:"at"`
}

func cloneProbeBudget(values map[string]int64) map[string]int64 {
	copy := make(map[string]int64, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func (r *Runtime) probeCeilingLocked(fallback int64) int64 {
	policies := r.config.Policies
	if r.constraints != nil {
		policies = r.constraints.Policies
	}
	for _, policy := range policies {
		fallback = max(fallback, policy.ProbeBudgetBytes)
	}
	if r.localV1 {
		fallback = min(fallback, int64(64<<20))
	}
	return fallback
}

func (r *Runtime) capacityBudgetStatusLocked(now time.Time) map[string]any {
	ceiling := r.probeCeilingLocked(0)
	total, capacity := r.reservedProbeBudgetLocked(), r.capacityReservedLocked(now)
	globalRemaining := max(int64(0), ceiling-total)
	remaining := max(int64(0), min(ceiling/2-capacity, globalRemaining))
	reason := "available"
	if remaining < 8<<20 {
		reason = "capacity_share_exhausted"
		if globalRemaining < 8<<20 {
			reason = "global_budget_exhausted"
		}
	}
	return map[string]any{"global_limit_bytes": ceiling, "global_remaining_bytes": globalRemaining,
		"capacity_reserved_bytes": capacity, "capacity_limit_bytes": ceiling / 2,
		"capacity_remaining_bytes": remaining, "capacity_probe_state": reason}
}

// An absent breakdown is legacy/unclassified spending. Treat all of it as
// capacity use rather than inventing additional reservations after an upgrade.
func (r *Runtime) capacityReservedLocked(now time.Time) int64 {
	if !now.UTC().Truncate(24 * time.Hour).Equal(r.probeWindow.UTC().Truncate(24 * time.Hour)) {
		return 0
	}
	values := r.capacityBudget
	if values == nil {
		values = r.budget
	}
	var used int64
	for _, value := range values {
		used += value
	}
	return used
}

func (r *Runtime) restoreProbeBudget(day time.Time, total, capacity map[string]int64, version int) {
	r.probeWindow = day
	r.budget = cloneProbeBudget(total)
	if version != 1 {
		r.capacityBudget = cloneProbeBudget(total)
	} else {
		r.capacityBudget = cloneProbeBudget(capacity)
	}
}

func capacityHintKey(e *engineEntry, c config.Config, speedURL string) string {
	if e.key == "" || speedURL == "" {
		return ""
	}
	source := ""
	if path, ok := c.Egress(e.route.Config.EgressID); ok {
		source = path.SourceIP
	}
	// e.key includes the route/core content identity and the checked dial IP.
	// Hash the target too; never persist a URL/token in the advisory cache.
	digest := sha256.Sum256([]byte(e.key + "\x00" + source + "\x00" + speedURL))
	return hex.EncodeToString(digest[:])
}

func validCapacityHint(key string, hint capacityHint, now time.Time) bool {
	if len(key) != 64 || hint.BPS <= 0 || hint.BPS > 1<<50 || hint.At <= 0 || hint.At > now.Unix() {
		return false
	}
	if _, err := hex.DecodeString(key); err != nil {
		return false
	}
	return now.Unix()-hint.At < int64(capacityHintLifetime/time.Second)
}

func (r *Runtime) capacitySnapshotLocked(now time.Time) map[string]capacityHint {
	copy := map[string]capacityHint{}
	for key, hint := range r.capacityHints {
		if validCapacityHint(key, hint, now) && len(copy) < processLimit*2 {
			copy[key] = hint
		}
	}
	return copy
}

func (r *Runtime) rememberCapacityLocked(e *engineEntry, c config.Config, target string, bps int64, now time.Time) {
	key := capacityHintKey(e, c, target)
	hint := capacityHint{BPS: bps, At: now.Unix()}
	if !validCapacityHint(key, hint, now) {
		return
	}
	if r.capacityHints == nil {
		r.capacityHints = map[string]capacityHint{}
	}
	for id, value := range r.capacityHints {
		if !validCapacityHint(id, value, now) {
			delete(r.capacityHints, id)
		}
	}
	if _, ok := r.capacityHints[key]; !ok && len(r.capacityHints) >= processLimit*2 {
		oldest := ""
		for id, value := range r.capacityHints {
			if oldest == "" || value.At < r.capacityHints[oldest].At || value.At == r.capacityHints[oldest].At && id < oldest {
				oldest = id
			}
		}
		delete(r.capacityHints, oldest)
	}
	r.capacityHints[key] = hint
}

func (r *Runtime) restoreEntryCapacityLocked(e *engineEntry, c config.Config, now time.Time) {
	for _, policy := range c.Policies {
		if policy.DisableSpeedProbes || policy.SpeedURL == "" {
			continue
		}
		belongs := false
		for _, id := range policy.Routes {
			belongs = belongs || id == e.route.Config.ID
		}
		if !belongs {
			continue
		}
		key := capacityHintKey(e, c, policy.SpeedURL)
		hint, ok := r.capacityHints[key]
		if ok && validCapacityHint(key, hint, now) && hint.At > e.route.LastSpeed.Load() {
			e.route.SpeedBPS.Store(hint.BPS)
			e.route.LastSpeed.Store(hint.At)
		}
	}
}

func validCapacityBudget(total, capacity map[string]int64) bool {
	if len(total) > 4096 || len(capacity) > 4096 {
		return false
	}
	for id, value := range total {
		if value < 0 || value > 1<<50 || len(id) > 128 || strings.ContainsRune(id, '\x00') {
			return false
		}
	}
	for id, value := range capacity {
		if value < 0 || value > total[id] {
			return false
		}
	}
	return true
}
