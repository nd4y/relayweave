package worker

import (
	"testing"
	"time"
)

func TestFailureAfterSelectionRejectsNewCandidateTicketsImmediately(t *testing.T) {
	for _, phase := range []string{"canary", "active_draining"} {
		for _, fault := range []string{"health", "circuit", "protocol_failures", "completed_failures"} {
			t.Run(phase+"/"+fault, func(t *testing.T) {
				r, old, selected := rolloverFixture()
				plan := r.rollovers["a"]
				plan.state, plan.step, plan.serial, plan.durableSerial = phase, 3, 1, 1
				// Selection has already happened; periodic maintain is never run.
				switch fault {
				case "health":
					selected.Instance.Ready.Store(false)
				case "circuit":
					selected.BlockedUntil.Store(time.Now().Add(time.Minute).UnixNano())
				case "protocol_failures":
					for range 3 {
						selected.failed()
					}
				case "completed_failures":
					plan.failed = 5
				}
				for range 100 {
					ticket, err := r.reserveRoute("policy:p", selected)
					if err != nil {
						t.Fatal(err)
					}
					if ticket.route != old {
						ticket.release()
						t.Fatal("failed candidate admitted before maintenance")
					}
					ticket.release()
				}
				if selected.References.Load() != 0 || old.References.Load() != 0 {
					t.Fatal("reference leaked")
				}
			})
		}
	}
}

func TestFailedCandidateCannotReviveUnhealthyOrUnauthorizedOldRevision(t *testing.T) {
	for _, oldFault := range []string{"unhealthy", "revoked"} {
		t.Run(oldFault, func(t *testing.T) {
			r, old, next := rolloverFixture()
			plan := r.rollovers["a"]
			plan.state, plan.serial, plan.durableSerial = "active_draining", 1, 1
			next.Instance.Ready.Store(false)
			if oldFault == "unhealthy" {
				old.Instance.Ready.Store(false)
			} else {
				c := r.config
				c.Routes = append(c.Routes[:0:0], c.Routes...)
				c.Routes[0].Previous = nil
				r.Restrict(c)
			}
			if ticket, err := r.reserveRoute("policy:p", next); err == nil {
				ticket.release()
				t.Fatal("unavailable fallback admitted")
			}
			if next.References.Load() != 0 || old.References.Load() != 0 {
				t.Fatal("reference leaked")
			}
		})
	}
}

func TestRotatedAndDNSCandidatesRequireThreeChecksAtTicketBoundary(t *testing.T) {
	r, old, next := rolloverFixture()
	plan := r.rollovers["a"]
	plan.state, plan.step, plan.serial, plan.durableSerial = "active_draining", 3, 1, 1
	next.Config.CheckServer = true
	// Isolate protocol readiness from the server-policy admission fence.
	old.Config.CheckServer = true
	old.Instance.ProbeSamples.Store(3)
	c := r.config
	c.Routes = append(c.Routes[:0:0], c.Routes...)
	c.Routes[0].CheckServer = true
	r.Restrict(c)
	for _, checks := range []int64{1, 2, 3} {
		next.Instance.ProbeSamples.Store(checks)
		ticket, err := r.reserveRoute("policy:p", next)
		if err != nil {
			t.Fatal(err)
		}
		want := old
		if checks == 3 {
			want = next
		}
		if ticket.route != want {
			ticket.release()
			t.Fatalf("checks=%d wrong revision", checks)
		}
		ticket.release()
	}
	// DNS-only replacement has no different catalog fingerprint/Previous grant.
	next.Config.Previous = nil
	next.Instance.ProbeSamples.Store(1)
	if r.safeRevisionAdmissionLocked(next, time.Now()) == next {
		t.Fatal("DNS candidate bypassed protocol gate")
	}
}

func TestHealthFailureDoesNotRevokeAnAlreadyIssuedAuthorizedTicket(t *testing.T) {
	r, old, next := rolloverFixture()
	ticket, err := r.reserveRoute("policy:p", next)
	if err != nil {
		t.Fatal(err)
	}
	defer ticket.release()
	ticket.route.Instance.Ready.Store(false)
	if !ticket.allowed() {
		t.Fatal("health failure changed established continuation authorization")
	}
	old.Instance.Ready.Store(false)
}

func TestUDPCapabilityFallbackCannotAdmitAnUnhealthyOldEngine(t *testing.T) {
	r, old, next := rolloverFixture()
	plan := r.rollovers["a"]
	plan.state, plan.step, plan.serial, plan.durableSerial = "active_draining", 3, 1, 1
	old.Config.UDP = true
	old.Instance.Ready.Store(false)
	if ticket, err := r.reserveRouteCapability("policy:p", next, true); err == nil {
		ticket.release()
		t.Fatal("UDP fallback skipped health gate")
	}
	if old.References.Load() != 0 || next.References.Load() != 0 {
		t.Fatal("rejected UDP ticket retained a reference")
	}
}

func TestExpiredCircuitRequiresFreshProofButDoesNotBecomePermanent(t *testing.T) {
	r, _, next := rolloverFixture()
	plan := r.rollovers["a"]
	plan.state, plan.step, plan.serial, plan.durableSerial = "active_draining", 3, 1, 1
	next.ConsecutiveFailures.Store(3)
	next.BlockedUntil.Store(time.Now().Add(-time.Second).UnixNano())
	if ticket, err := r.reserveRoute("policy:p", next); err != nil {
		t.Fatal(err)
	} else {
		ticket.release()
		if ticket.route == next {
			t.Fatal("cooldown timer fabricated recovery proof")
		}
	}
	next.Instance.ProbeSamples.Add(1)
	if ticket, err := r.reserveRoute("policy:p", next); err != nil {
		t.Fatal(err)
	} else {
		defer ticket.release()
		if ticket.route != next {
			t.Fatal("expired circuit permanently blocked healthy candidate")
		}
	}
}
