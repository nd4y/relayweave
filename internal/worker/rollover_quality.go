package worker

import (
	"sort"
	"time"
)

const qualityWindow = 60 * time.Second
const qualitySampleLimit = 64
const qualityHTTPReserve = int64(32 << 10)
const qualityBulkPayload = int64(1 << 20)
const qualityBulkReserve = qualityBulkPayload + (64 << 10)

// Only controlled HTTP requests produce TTFB. Opaque CONNECT/TLS application
// bytes and user think time are deliberately absent from these observations.
type qualityMeasurement struct {
	OK                           bool
	ConnectUS, TTFBUS, ElapsedUS int64
	Bytes, BackgroundBPS         int64
}

type qualityPair struct {
	At          time.Time
	Bulk        bool
	OldEligible bool
	Old, Next   qualityMeasurement
}

type qualityVerdict struct {
	Allow, Pause                        bool
	Reason, Mode, Confidence, BulkState string
	Samples, PairedSamples              int
	BulkRatio                           float64
}

type rolloverQuality struct {
	Signature                       [32]byte
	LastAttempt, LastHTTP, LastBulk time.Time
	Samples                         []qualityPair
	Blocked                         bool
	Reason                          string
	InFlight                        bool
	TargetIndex                     int
}

func (q *rolloverQuality) add(pair qualityPair) {
	if len(q.Samples) == qualitySampleLimit {
		copy(q.Samples, q.Samples[1:])
		q.Samples[len(q.Samples)-1] = pair
	} else {
		q.Samples = append(q.Samples, pair)
	}
}

func qualityP95(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	// Nearest-rank empirical percentile, explicitly not a confidence guarantee.
	index := (95*len(values)+99)/100 - 1
	return values[max(0, min(index, len(values)-1))]
}

func qualityLatencyRegressed(old, next []int64) bool {
	baseline := qualityP95(old)
	return qualityP95(next) > baseline+max(baseline/5, int64(20000))
}

func qualityLoadsComparable(old, next int64) bool {
	delta := old - next
	if delta < 0 {
		delta = -delta
	}
	return delta <= max(int64(64<<10), max(old, next)*15/100)
}

// Evaluation is pure and clock-injectable. Samples are bounded, scoped by an
// engine/probe signature and accepted only after a post-I/O fence recheck.
func (q *rolloverQuality) evaluate(now time.Time, speedConfigured, speedDisabled bool) qualityVerdict {
	v := qualityVerdict{Reason: "waiting_quality_samples", Mode: "evaluating", Confidence: "insufficient", BulkState: "waiting"}
	if q == nil {
		return v
	}
	var oldConnect, nextConnect, oldTTFB, nextTTFB []int64
	var bulk []qualityPair
	nextGood, nextBad, oldBad, recoveryStreak := 0, 0, 0, 0
	for _, s := range q.Samples {
		if s.At.IsZero() || now.Sub(s.At) < 0 || now.Sub(s.At) > qualityWindow {
			continue
		}
		if s.Bulk {
			bulk = append(bulk, s)
			continue
		}
		v.Samples++
		if s.Next.OK {
			nextGood++
		} else {
			nextBad++
		}
		if !s.Old.OK {
			oldBad++
		}
		if s.Next.OK && (!s.OldEligible || !s.Old.OK) {
			recoveryStreak++
		} else {
			recoveryStreak = 0
		}
		if s.OldEligible && s.Old.OK && s.Next.OK {
			oldConnect = append(oldConnect, s.Old.ConnectUS)
			nextConnect = append(nextConnect, s.Next.ConnectUS)
			oldTTFB = append(oldTTFB, s.Old.TTFBUS)
			nextTTFB = append(nextTTFB, s.Next.TTFBUS)
		}
	}
	v.PairedSamples = len(oldConnect)
	if nextGood < 3 {
		if nextBad >= 5 && len(oldConnect)+oldBad < v.Samples {
			v.Pause = true
			v.Reason = "candidate_control_failures"
		}
		return v
	}
	if v.Samples >= 20 {
		if float64(nextGood)/float64(v.Samples) < .95 || float64(nextBad-oldBad)/float64(v.Samples) > .01 {
			v.Pause = true
			v.Reason = "candidate_control_error_regression"
			return v
		}
	} else if nextBad != 0 {
		v.Reason = "waiting_clean_control_window"
		return v
	}
	if recoveryStreak >= 3 {
		v.Mode = "verified_recovery"
		v.Reason = "baseline_unavailable"
	} else if v.PairedSamples >= 3 {
		v.Mode = "paired"
		if qualityLatencyRegressed(oldConnect, nextConnect) || qualityLatencyRegressed(oldTTFB, nextTTFB) {
			v.Pause = true
			v.Reason = "paired_latency_regression"
			return v
		}
	} else {
		v.Reason = "waiting_old_baseline"
		return v
	}
	if v.PairedSamples >= 20 {
		v.Confidence = "observed_window"
	} else {
		v.Confidence = "limited_sample_guard"
	}
	if speedDisabled {
		v.Allow = true
		v.BulkState = "disabled_by_policy"
		if v.Mode == "paired" {
			v.Reason = "quality_ready"
		}
		return v
	}
	if !speedConfigured {
		v.Reason = "waiting_speed_target"
		return v
	}
	var ratios []float64
	recoveryBulk := false
	for _, s := range bulk {
		if v.Mode == "verified_recovery" && (!s.OldEligible || !s.Old.OK) {
			recoveryBulk = s.Next.OK && s.Next.Bytes >= qualityBulkPayload && s.Next.ElapsedUS > 0
			continue
		}
		if !s.OldEligible || !s.Old.OK || s.Old.Bytes < qualityBulkPayload || s.Old.ElapsedUS <= 0 || !qualityLoadsComparable(s.Old.BackgroundBPS, s.Next.BackgroundBPS) {
			continue
		}
		if !s.Next.OK || s.Next.Bytes < qualityBulkPayload || s.Next.ElapsedUS <= 0 {
			ratios = append(ratios, 0)
			continue
		}
		ratio := (float64(s.Next.Bytes) / float64(s.Next.ElapsedUS)) / (float64(s.Old.Bytes) / float64(s.Old.ElapsedUS))
		ratios = append(ratios, ratio)
	}
	if v.Mode == "verified_recovery" && recoveryBulk {
		v.Allow = true
		v.BulkState = "baseline_unavailable"
		return v
	}
	if len(ratios) == 0 {
		v.Reason = "waiting_comparable_bulk"
		return v
	}
	v.BulkRatio = ratios[len(ratios)-1]
	if v.BulkRatio < .9 {
		v.Reason = "waiting_bulk_recheck"
		if len(ratios) >= 2 && ratios[len(ratios)-2] < .9 {
			v.Pause = true
			v.Reason = "paired_bulk_regression"
		}
		return v
	}
	v.Allow = true
	v.BulkState = "paired_lower_bound"
	if v.Mode == "paired" {
		v.Reason = "quality_ready"
	}
	return v
}
