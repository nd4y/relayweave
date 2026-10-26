package worker

import "time"

func locallyFailedForAdmission(v *Route) bool {
	return v.ConsecutiveFailures.Load() >= 3 && (v.Instance == nil || !v.Instance.Ready.Load() || v.Instance.ProbeSamples.Load() <= v.FailureProbeSamples.Load())
}

// Eligibility must be checked where the ticket is issued, not only in the
// periodically rebuilt shortlist. Health can fail after selection and before
// reservation. This restricts new tickets; established tickets keep their
// security/authorization continuation contract independently of path health.
func (r *Runtime) revisionReadyForAdmissionLocked(v *Route, now time.Time) bool {
	if v == nil || v.Instance == nil || !v.Instance.Ready.Load() || locallyFailedForAdmission(v) || now.UnixNano() < v.BlockedUntil.Load() {
		return false
	}
	plan := r.rollovers[v.Config.ID]
	rotated := len(v.Config.Previous) > 0 || (plan != nil && plan.next == v)
	return !v.Config.CheckServer || !rotated || v.Instance.ProbeSamples.Load() >= 3
}

func (r *Runtime) safeRevisionAdmissionLocked(v *Route, now time.Time) *Route {
	if v == nil {
		return nil
	}
	plan := r.rollovers[v.Config.ID]
	failed := plan != nil && plan.next == v && (plan.failed >= 5 || (plan.quality != nil && plan.quality.Blocked))
	if !failed && r.revisionReadyForAdmissionLocked(v, now) {
		return v
	}
	if plan != nil && plan.next == v && r.revisionReadyForAdmissionLocked(plan.old, now) && r.overlapAllowedLocked("", plan.old.Config) {
		return plan.old
	}
	return nil
}
