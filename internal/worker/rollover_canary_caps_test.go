package worker

import (
	"proxyplatform/internal/config"
	"testing"
	"time"
)

func sharedCanaryFixture() *Runtime {
	r, old, next := rolloverFixture()
	old.Config.ServerHost = "old-a.test"
	next.Config.ServerHost = "new-a.test"
	c := r.config
	c.Routes = append(c.Routes[:0:0], next.Config)
	c.Policies = []config.Policy{{ID: "p", Routes: []string{"a", "b", "c"}}}
	for _, id := range []string{"b", "c"} {
		o, n := readyRoute(id), readyRoute(id)
		o.Config, n.Config = old.Config, next.Config
		o.Config.ID, n.Config.ID = id, id
		o.Config.ServerHost, n.Config.ServerHost = "old-"+id+".test", "new-"+id+".test"
		r.entries["old-"+id], r.entries["next-"+id] = &engineEntry{route: o}, &engineEntry{route: n}
		r.current[id] = r.entries["next-"+id]
		r.rollovers[id] = &pathRollover{old: o, next: n, state: "canary"}
		c.Routes = append(c.Routes, n.Config)
	}
	r.Restrict(c)
	r.config = c
	r.endpointIPs = map[string]endpointResolution{}
	for _, id := range []string{"a", "b", "c"} {
		r.endpointIPs["new-"+id+".test"] = endpointResolution{IPs: []string{"8.8.8.8"}, Observed: time.Now()}
	}
	return r
}

func TestAliasesShareFirstCanaryRateIncludingRetries(t *testing.T) {
	r := sharedCanaryFixture()
	now := time.Now()
	accepted := 0
	for i := 0; i < 10000; i++ {
		id := []string{"a", "b", "c"}[i%3]
		p := r.rollovers[id]
		if r.chooseRevisionLocked(p.next, now) == p.next {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("aliases/retries amplified one-admission/s allowance: %d", accepted)
	}
	for _, p := range r.rollovers {
		p.sequence = 0
	}
	if r.chooseRevisionLocked(r.rollovers["b"].next, now.Add(time.Second)) != r.rollovers["b"].next {
		t.Fatal("allowance did not recover after one second")
	}
}

func TestAliasesShareInFlightReferencesWithoutInterruptingHeldTickets(t *testing.T) {
	r := sharedCanaryFixture()
	a, err := r.reserveRoute("policy:p", r.current["a"].route)
	if err != nil || a.route != r.rollovers["a"].next {
		t.Fatal("first admission failed", err)
	}
	defer a.release()
	// Advance just the rate clock; real reservation references stay held.
	for _, p := range r.rollovers {
		p.lastAdmission = time.Now().Add(-2 * time.Second)
	}
	b, err := r.reserveRoute("policy:p", r.current["b"].route)
	if err != nil || b.route != r.rollovers["b"].next {
		t.Fatal("second admission failed", err)
	}
	defer b.release()
	for _, p := range r.rollovers {
		p.lastAdmission = time.Now().Add(-2 * time.Second)
	}
	c, err := r.reserveRoute("policy:p", r.current["c"].route)
	if err != nil {
		t.Fatal(err)
	}
	defer c.release()
	if c.route != r.rollovers["c"].old {
		t.Fatal("third alias bypassed aggregate in-flight cap")
	}
	if !a.allowed() || !b.allowed() {
		t.Fatal("first-stage cap revoked an accepted ticket")
	}
}

func TestAliasesSharePayloadWindowAndKeepIndependentISPAllowance(t *testing.T) {
	r := sharedCanaryFixture()
	a, b, c := r.rollovers["a"], r.rollovers["b"], r.rollovers["c"]
	now := time.Now()
	if !r.firstCanaryAdmissionAllowedLocked(a, now) {
		t.Fatal("initial allowance missing")
	}
	a.next.BytesDown.Store(5 << 20)
	b.next.BytesUp.Store(3 << 20)
	if r.firstCanaryAdmissionAllowedLocked(c, now.Add(2*time.Second)) {
		t.Fatal("aliases amplified payload budget")
	}
	c.old.Config.EgressID, c.next.Config.EgressID = "isp2", "isp2"
	if !r.firstCanaryAdmissionAllowedLocked(c, now.Add(2*time.Second)) {
		t.Fatal("one ISP consumed another ISP's allowance")
	}
	if !r.firstCanaryAdmissionAllowedLocked(a, now.Add(30*time.Second)) {
		t.Fatal("group payload window did not recover")
	}
	if !a.windowAt.Equal(b.windowAt) {
		t.Fatal("group payload windows diverged")
	}
}

func TestTransitiveStaleDNSAndPinnedIPDoNotInventIndependentCanaries(t *testing.T) {
	r := sharedCanaryFixture()
	now := time.Now()
	r.endpointIPs["new-a.test"] = endpointResolution{IPs: []string{"8.8.8.8", "1.1.1.1"}, Observed: now.Add(-time.Hour)}
	r.endpointIPs["new-b.test"] = endpointResolution{IPs: []string{"1.1.1.1", "9.9.9.9"}, Observed: now.Add(-time.Hour)}
	r.endpointIPs["new-c.test"] = endpointResolution{IPs: []string{"9.9.9.9"}, Observed: now.Add(-time.Hour)}
	if peers := r.firstCanaryPeersLocked(r.rollovers["a"], now); len(peers) != 3 {
		t.Fatal("stale/transitive evidence invented independent budget", len(peers))
	}
	r.endpointIPs = nil
	for _, e := range r.entries {
		if e.route == r.rollovers["a"].next || e.route == r.rollovers["b"].next || e.route == r.rollovers["c"].next {
			e.dialIP = "8.8.4.4"
		}
	}
	if peers := r.firstCanaryPeersLocked(r.rollovers["a"], now); len(peers) != 3 {
		t.Fatal("actual pinned sockets ignored", len(peers))
	}
}

func TestMergingCanaryGroupsPreservesAlreadySpentPayload(t *testing.T) {
	r := sharedCanaryFixture()
	now := time.Now()
	a, b := r.rollovers["a"], r.rollovers["b"]
	r.endpointIPs["new-b.test"] = endpointResolution{IPs: []string{"4.2.2.2"}, Observed: now}
	r.endpointIPs["new-c.test"] = endpointResolution{IPs: []string{"4.2.2.3"}, Observed: now}
	r.firstCanaryAdmissionAllowedLocked(a, now)
	r.firstCanaryAdmissionAllowedLocked(b, now.Add(10*time.Second))
	a.next.BytesDown.Store(7 << 20)
	b.next.BytesDown.Store(1 << 20)
	r.endpointIPs["new-b.test"] = endpointResolution{IPs: []string{"8.8.8.8"}, Observed: now.Add(20 * time.Second)}
	if r.firstCanaryAdmissionAllowedLocked(b, now.Add(20*time.Second)) {
		t.Fatal("group merge reset already-spent byte budget")
	}
	if !a.windowAt.Equal(b.windowAt) {
		t.Fatal("merged group did not synchronize its window")
	}
}
