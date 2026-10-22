package worker

import (
	"proxyplatform/internal/config"
	"sync"
	"testing"
	"time"
)

func rolloverFixture() (*Runtime, *Route, *Route) {
	r := selectionFixture("auto")
	old := r.current["a"].route
	old.Config.Fingerprint = "11111111111111111111111111111111"
	old.Config.Core = "xray"
	old.Config.ValidUntil = time.Now().Add(time.Hour)
	next := readyRoute("a")
	next.Config = old.Config
	next.Config.Fingerprint = "22222222222222222222222222222222"
	next.Config.Previous = []config.RevisionPermit{{Fingerprint: old.Config.Fingerprint, ValidUntil: old.Config.ValidUntil}}
	c := config.Config{Routes: []config.Route{next.Config}, Policies: []config.Policy{{ID: "p", Routes: []string{"a"}}}}
	r.Restrict(c)
	r.config = c
	r.entries = map[string]*engineEntry{"old": {route: old}, "new": {route: next}}
	r.current = map[string]*engineEntry{"a": r.entries["new"]}
	r.rollovers = map[string]*pathRollover{"a": {old: old, next: next, state: "canary", started: time.Now(), changed: time.Now()}}
	return r, old, next
}

func TestCanaryUsesAdmissionTicketsAndRevocationOverridesOverlap(t *testing.T) {
	r, old, next := rolloverFixture()
	count := 0
	for range 10000 {
		ticket, err := r.reserveRoute("policy:p", next)
		if err != nil {
			t.Fatal(err)
		}
		if ticket.route == next {
			count++
		}
		if ticket.route.References.Load() != 1 {
			t.Fatal("dial not pinned")
		}
		ticket.release()
		ticket.release()
	}
	if count < 1 || count > 500 || old.References.Load() != 0 || next.References.Load() != 0 {
		t.Fatal("canary cap or ref leak", count)
	}
	ticket, err := r.reserveRoute("policy:p", old)
	if err != nil {
		t.Fatal(err)
	}
	r.RestrictLiveGuard(config.LiveGuard{Version: 2, Routes: map[string]config.RoutePermit{}})
	if ticket.allowed() {
		t.Fatal("security revoke bypassed by continuation")
	}
	ticket.release()
}

func TestConcurrentTicketsRemainBoundedAcrossRevocation(t *testing.T) {
	r, _, next := rolloverFixture()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 1000 {
				if ticket, err := r.reserveRoute("policy:p", next); err == nil {
					ticket.allowed()
					ticket.release()
				}
			}
		}()
	}
	r.RestrictLiveGuard(config.LiveGuard{Version: 3, Routes: map[string]config.RoutePermit{}})
	wg.Wait()
	for _, e := range r.entries {
		if e.route.References.Load() != 0 {
			t.Fatal("reference leak")
		}
	}
}

func TestLowTrafficCannotPromoteByTimerAndFailureRollsBack(t *testing.T) {
	r, old, next := rolloverFixture()
	p := r.rollovers["a"]
	p.started = time.Now().Add(-31 * time.Minute)
	p.changed = p.started
	r.advanceRolloversLocked(time.Now())
	if p.state != "paused" || r.chooseRevisionLocked(next, time.Now()) != old {
		t.Fatal("timer promoted unproven candidate")
	}
	p.state = "canary"
	p.failed = 5
	r.advanceRolloversLocked(time.Now())
	if p.state != "rolled_back" || r.chooseRevisionLocked(next, time.Now()) != old {
		t.Fatal("failed candidate still admitted")
	}
}

func TestOrdinaryRemovalDrainsButOwnerRestrictionStillRevokes(t *testing.T) {
	r, _, next := rolloverFixture()
	ticket, err := r.reserveRoute("policy:p", next)
	if err != nil {
		t.Fatal(err)
	}
	defer ticket.release()
	c := r.config
	c.Routes = append([]config.Route(nil), c.Routes...)
	c.Routes[0].EstablishedOnly = true
	r.Restrict(c)
	r.RestrictLiveGuard(config.LiveGuard{Version: 4, Routes: map[string]config.RoutePermit{"a": {EstablishedOnly: true, Fingerprint: next.Config.Fingerprint, ValidUntil: next.Config.ValidUntil, Previous: next.Config.Previous}}})
	if !ticket.allowed() {
		t.Fatal("ordinary removal interrupted an established session")
	}
	if admitted, err := r.reserveRoute("policy:p", next); err == nil {
		admitted.release()
		t.Fatal("removed profile admitted new traffic")
	}
	r.Restrict(config.Config{Policies: []config.Policy{{ID: "p"}}})
	if ticket.allowed() {
		t.Fatal("continuation bypassed explicit policy exclusion")
	}
}

func TestPromotionWaitsForDurableJournal(t *testing.T) {
	r, old, next := rolloverFixture()
	p := r.rollovers["a"]
	p.serial = 2
	p.durableSerial = 1
	p.step = 2
	for range 100 {
		if r.chooseRevisionLocked(next, time.Now()) != old {
			t.Fatal("uncheckpointed promotion admitted candidate")
		}
	}
	records := r.rolloverRecordsLocked()
	r.acknowledgeRollovers(records)
	if p.durableSerial != 2 {
		t.Fatal("durable ack not installed")
	}
	seen := false
	for range 100 {
		seen = seen || r.chooseRevisionLocked(next, time.Now()) == next
	}
	if !seen {
		t.Fatal("checkpointed candidate stayed blocked")
	}
}
