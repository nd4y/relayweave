package worker

import (
	"proxyplatform/internal/config"
	"testing"
	"time"
)

func TestSupersedingCanaryPreservesBaselineAndDrainsIntermediateTickets(t *testing.T) {
	r, baseline, intermediate := rolloverFixture()
	p := r.rollovers["a"]
	p.step = 3
	oldTicket, err := r.reserveRoute("policy:p", baseline)
	// chooseRevision operates on the logical route, so reserve baseline while
	// explicitly warming, then take an intermediate ticket at 100% for this
	// lifecycle fixture. This is no claim of native TCP/UDP coverage.
	if err != nil {
		t.Fatal(err)
	}
	oldTicket.release()
	p.state = "warming"
	oldTicket, err = r.reserveRoute("policy:p", baseline)
	if err != nil {
		t.Fatal(err)
	}
	defer oldTicket.release()
	p.state = "canary"
	newTicket, err := r.reserveRoute("policy:p", intermediate)
	if err != nil {
		t.Fatal(err)
	}
	defer newTicket.release()
	if oldTicket.route != baseline || newTicket.route != intermediate {
		t.Fatal("fixture tickets wrong")
	}
	next := readyRoute("a")
	next.Config = intermediate.Config
	next.Config.Fingerprint = "33333333333333333333333333333333"
	next.Config.Previous = []config.RevisionPermit{{Fingerprint: baseline.Config.Fingerprint, ValidUntil: baseline.Config.ValidUntil}, {Fingerprint: intermediate.Config.Fingerprint, ValidUntil: intermediate.Config.ValidUntil}}
	previous := r.current
	r.entries["third"] = &engineEntry{route: next}
	r.current = map[string]*engineEntry{"a": r.entries["third"]}
	c := r.config
	c.Routes = []config.Route{next.Config}
	r.Restrict(c)
	r.installRolloversLocked(previous, time.Now())
	if r.rollovers["a"].old != baseline || r.rollovers["a"].next != next {
		t.Fatal("unpromoted intermediate became last-known-good")
	}
	if !oldTicket.allowed() || !newTicket.allowed() || intermediate.References.Load() != 1 {
		t.Fatal("superseding interrupted allowed drain refs")
	}
	c.Routes[0].Previous = c.Routes[0].Previous[1:]
	r.Restrict(c)
	if oldTicket.allowed() || !newTicket.allowed() {
		t.Fatal("explicit baseline revoke ignored or intermediate unnecessarily revoked")
	}
}

func TestV2ProjectionStopsNewTicketsPreservingFrozenContinuation(t *testing.T) {
	r, old, next := rolloverFixture()
	p := r.rollovers["a"]
	p.state = "warming"
	ticket, err := r.reserveRoute("policy:p", old)
	if err != nil {
		t.Fatal(err)
	}
	defer ticket.release()
	guard := config.LiveGuard{Version: 3, Routes: map[string]config.RoutePermit{next.Config.Profile(): {Fingerprint: next.Config.Fingerprint, ValidUntil: next.Config.ValidUntil, Previous: append([]config.RevisionPermit(nil), next.Config.Previous...)}}}
	r.RestrictLiveGuard(guard.ForRoutingVersion(2))
	if !ticket.allowed() {
		t.Fatal("v2 upgrade projection interrupted authorized continuation")
	}
	if extra, e := r.reserveRoute("policy:p", next); e == nil {
		extra.release()
		t.Fatal("v2 projection admitted new ticket")
	}
	guard.Version++
	guard.Routes = map[string]config.RoutePermit{}
	r.RestrictLiveGuard(guard.ForRoutingVersion(2))
	if ticket.allowed() {
		t.Fatal("v2 continuation bypassed explicit revoke")
	}
}
