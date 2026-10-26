package worker

import (
	"proxyplatform/internal/config"
	"testing"
	"time"
)

func TestPolicyTargetHealthIsIndependentAndExpires(t *testing.T) {
	r := selectionFixture("throughput")
	r.config.ProbeURL = "https://generic.example/health"
	p := r.policies["p"]
	p.rank = map[string]int{"a": 0, "b": 1}
	p.config.ProbeURL = "https://application.example/health"
	p.config.ProbeIntervalSeconds = 10
	r.installTargetObservationsLocked(map[string]targetObservation{
		"p/a": {Signature: targetSignature(p.config), Checked: time.Now(), Healthy: true},
		"p/b": {Signature: targetSignature(p.config), Checked: time.Now(), Healthy: false},
	})
	r.rebuildLocked()
	paths := r.Candidates("policy:p")
	if len(paths) != 1 || paths[0].Config.ID != "a" {
		t.Fatal("generic core health bypassed failed target")
	}
	if !r.current["b"].route.Instance.Ready.Load() {
		t.Fatal("site failure quarantined shared core")
	}
	if r.policyTargetReadyLocked(p.config, "a", time.Now().Add(61*time.Second)) {
		t.Fatal("stale application health stayed eligible")
	}
	old := p.config
	p.config.ProbeURL = "https://changed.example/health"
	if r.policyTargetReadyLocked(p.config, "a", time.Now()) {
		t.Fatal("changed target reused old success")
	}
	p.config = old
	r.installTargetObservationsLocked(map[string]targetObservation{"p/b": {Signature: targetSignature(old), Checked: time.Now(), Healthy: true}})
	r.rebuildLocked()
	if len(r.Candidates("policy:p")) != 2 {
		t.Fatal("recovered target never rejoined")
	}
	generic := config.Policy{ID: "generic", ProbeURL: r.config.ProbeURL}
	r.targetHealth["generic/b"] = targetObservation{Signature: targetSignature(generic), Checked: time.Now(), Healthy: true}
	if !r.policyTargetReadyLocked(generic, "b", time.Now()) {
		t.Fatal("unrelated generic policy inherited site rejection")
	}
	r.policies["generic"] = &policyState{config: generic}
	r.config.ProbeURL = p.config.ProbeURL
	r.targetHealth["p/b"] = targetObservation{Signature: targetSignature(p.config), Checked: time.Now(), Healthy: false}
	if r.policyTargetReadyLocked(p.config, "b", time.Now()) {
		t.Fatal("first policy in configuration bypassed its application probe")
	}
	p.config.ProbeScope = "shared"
	if !r.policyTargetReadyLocked(p.config, "b", time.Now()) {
		t.Fatal("shared connectivity scope inherited an unrelated website rejection")
	}
	p.config.ProbeScope = "target"
	if r.policyTargetReadyLocked(p.config, "b", time.Now()) {
		t.Fatal("explicit target scope bypassed failed application probe")
	}
	delete(r.policies, "p")
	delete(r.policies, "generic")
	r.installTargetObservationsLocked(nil)
	if len(r.targetHealth) != 0 {
		t.Fatal("deleted policy observations retained")
	}
}
