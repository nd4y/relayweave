package worker

import (
	"encoding/json"
	"math"
	"proxyplatform/internal/config"
	"proxyplatform/internal/topology"
	"strings"
	"testing"
	"time"
)

func TestScoreEvidenceContainsActualOperandsAndSurvivesMetricChanges(t *testing.T) {
	route := readyRoute("trace")
	route.RTTUS.Store(25000)
	route.Active.Store(3)
	route.ConsecutiveFailures.Store(2)
	route.ErrorEWMA.Store(125)
	route.LastSpeed.Store(time.Now().Unix())
	route.SpeedBPS.Store(25000000)
	evidence := topology.Candidate{}
	score := route.scoreObserved("throughput", &evidence)
	expected := (1 + 3.0) * 12500000 / 25000000 * (1 + 25.0/20000) * (1 + 2.0) * (1 + 125.0/250)
	if math.Abs(score-expected) > 1e-10 || evidence.Active != 3 || evidence.EffectiveRTTMS != 25 || evidence.ConsecutiveFailures != 2 || evidence.ErrorEWMA != 125 || evidence.SpeedUsedBPS != 25000000 || evidence.SpeedSource != "active_sample" || evidence.Formula != "capacity_weighted_load" {
		t.Fatalf("wrong actual operands %+v score=%g expected=%g", evidence, score, expected)
	}
	route.Active.Store(999)
	route.ErrorEWMA.Store(999)
	if evidence.Active != 3 || evidence.ErrorEWMA != 125 {
		t.Fatal("evidence recomputed from later metrics")
	}
	route.LastSpeed.Store(time.Now().Add(-time.Hour).Unix())
	evidence = topology.Candidate{}
	route.scoreObserved("throughput", &evidence)
	if evidence.SpeedUsedBPS != 0 {
		t.Fatal("stale probe used")
	}
	route.LastPassive.Store(time.Now().Unix())
	route.PassiveBPS.Store(5000000)
	evidence = topology.Candidate{}
	route.scoreObserved("auto", &evidence)
	if evidence.SpeedSource != "delivered_lower_bound" || evidence.SpeedUsedBPS != 5000000 {
		t.Fatal("passive lower bound lost")
	}
}

func TestTopologyRecordsAutomaticWeightsWithoutChangingSelection(t *testing.T) {
	plain, observed := selectionFixture("throughput"), selectionFixture("throughput")
	for _, r := range []*Runtime{plain, observed} {
		r.config.Generation = 42
		r.current["a"].route.Active.Store(9)
		r.current["b"].route.Active.Store(1)
	}
	for n := 0; n < 30; n++ {
		d := topology.Decision{}
		got := observed.candidatesObserved("policy:p", &d)
		want := plain.Candidates("policy:p")
		if got[0].Config.ID != want[0].Config.ID {
			t.Fatal("instrumentation changed selection")
		}
		if d.Generation != 42 || d.Method != "automatic_profile_weights" || d.Mode != "throughput" {
			t.Fatalf("missing evidence %+v", d)
		}
		compared := 0
		for _, c := range d.Candidates {
			if c.Score != nil {
				compared++
			}
		}
		if compared != 2 {
				t.Fatal("expected actual compared profile scores")
		}
	}
}
func TestTopologyCapturesHardConstraintsAndUnknowns(t *testing.T) {
	r := selectionFixture("sticky")
	r.current["a"].route.BlockedUntil.Store(time.Now().Add(time.Minute).UnixNano())
	d := topology.Decision{}
	result := r.candidatesObserved("policy:p", &d)
	if len(result) != 1 || result[0].Config.ID != "b" {
		t.Fatal("constraint")
	}
	found := false
	for _, c := range d.Candidates {
		if c.Route == "a" && c.Reason == "circuit_open" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing circuit evidence")
	}
	r.lease.Store(1)
	d = topology.Decision{}
	r.candidatesObserved("policy:p", &d)
	if d.Reason != "lease_expired" {
		t.Fatal("lease")
	}
}
func TestTopologyPrivacyAllowlistAndCounters(t *testing.T) {
	r := selectionFixture("auto")
	r.entries = r.current
	r.config = config.Config{Generation: 3, Routes: []config.Route{{ID: "a"}}, Listeners: []config.Listener{{ID: "http", Protocol: "http", Username: "SECRET_USER", Password: "SECRET_PASSWORD"}}}
	for id, e := range r.current {
		e.key = id
		e.route.Config.Outbound = json.RawMessage(`{"password":"SECRET_OUTBOUND","url":"https://secret.invalid/?token=SECRET"}`)
		e.route.Config.ServerHost = "192.0.2.1"
		e.route.Config.CapacityGroup = "source/192.0.2.1"
		e.route.BytesDown.Store(123)
		e.route.BytesUp.Store(17)
	}
	g := &Gateway{Topology: topology.NewObserver()}
	f := r.TopologyFrame(g)
	b, _ := json.Marshal(f)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "secret.invalid") {
		t.Fatal("secret leak")
	}
	if len(f.Routes) != 2 || f.Routes[0].Down != 123 || f.Routes[0].Up != 17 || f.Routes[0].Group != f.Routes[1].Group {
		t.Fatalf("projection %+v", f)
	}
}

func TestTopologyDoesNotInventOutsideWindowOrManualCauses(t *testing.T) {
	r := selectionFixture("manual")
	r.policies["p"].config.Routes = []string{"a", "b"}
	d := topology.Decision{}
	r.candidatesObserved("policy:p", &d)
	found := false
	for _, c := range d.Candidates {
		if c.Route == "b" && c.Reason == "excluded_by_manual_pin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("manual exclusion missing %+v", d.Candidates)
	}
	r = selectionFixture("auto")
	r.policies["p"].best = r.policies["p"].best[:1]
	r.policies["p"].rank = map[string]int{"a": 0, "b": 1}
	r.current["b"].route.BlockedUntil.Store(time.Now().Add(time.Minute).UnixNano())
	d = topology.Decision{}
	r.candidatesObserved("policy:p", &d)
	found = false
	for _, c := range d.Candidates {
		if c.Route == "b" && c.Reason == "not_in_prepared_window" {
			found = true
		}
	}
	if !found {
		t.Fatalf("outside-window cause reconstructed %+v", d.Candidates)
	}
}

func TestTopologyPreparedWarmReserveIsSeparateFromEligibility(t *testing.T) {
	r := selectionFixture("auto")
	r.policies["p"].config.ActivePaths = 1
	r.policies["p"].config.WarmReserve = 1
	d := topology.Decision{}
	result := r.candidatesObserved("policy:p", &d)
	if len(result) != 2 {
		t.Fatal("reserve removed from native retries")
	}
	found := false
	for _, c := range d.Candidates {
		if c.Route == "b" {
			for _, factor := range c.Factors {
				if factor == "warm_reserve" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("prepared reserve role missing")
	}
}
