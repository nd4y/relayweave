package control

import (
	"encoding/json"
	"testing"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
	"proxyplatform/internal/store"
)

func TestRecoveryPreservesRevocations(t *testing.T) {
	old := config.Config{Cores: []config.Core{{ID: "old"}}, Routes: []config.Route{{ID: "same", Core: "old", Outbound: json.RawMessage(`{"credential":"same"}`)}, {ID: "revoked", Core: "old", Outbound: json.RawMessage(`{"credential":"old"}`)}, {ID: "removed", Core: "old", Outbound: json.RawMessage(`{}`)}}}
	desired := config.Config{Managed: true, Cores: []config.Core{{ID: "new"}}, Routes: []config.Route{{ID: "same", Core: "new", Outbound: json.RawMessage(`{"credential":"same"}`)}, {ID: "revoked", Core: "new", Outbound: json.RawMessage(`{"credential":"new"}`)}}, Policies: []config.Policy{{ID: "p", Routes: []string{"same", "revoked"}}}, Listeners: []config.Listener{{ID: "l", Password: "new-hash", AllowCIDRs: []string{"192.168.0.0/24"}}}}
	got := SafeRecovery(old, desired)
	if len(got.Routes) != 1 || got.Routes[0].ID != "same" || got.Routes[0].Core != "old" {
		t.Fatal("incompatible path resurrected")
	}
	if len(got.Policies[0].Routes) != 1 || got.Listeners[0].Password != "new-hash" || got.Listeners[0].AllowCIDRs[0] != "192.168.0.0/24" {
		t.Fatal("security change rolled back")
	}
	if len(desired.Policies[0].Routes) != 2 {
		t.Fatal("input mutated")
	}
}

func TestRecoveryRestoresKnownGoodProbeTargetsWithoutUndoingRevocations(t *testing.T) {
	previous := config.Config{Routes: []config.Route{{ID: "kept", Outbound: json.RawMessage(`{}`)}, {ID: "removed", Outbound: json.RawMessage(`{}`)}}, Policies: []config.Policy{{ID: "p", Routes: []string{"kept", "removed"}, ProbeURL: "https://good.invalid/", ProbeFallbackURLs: []string{"https://reserve.invalid/"}, ProbeIntervalSeconds: 30, ConnectTimeoutMS: 2500}}}
	desired := config.Config{Routes: []config.Route{{ID: "kept", Outbound: json.RawMessage(`{}`)}}, Policies: []config.Policy{{ID: "p", Routes: []string{"kept"}, ProbeURL: "https://bad.invalid/", ConnectTimeoutMS: 500}}, Listeners: []config.Listener{{Password: "new-password"}}}
	got := SafeRecovery(previous, desired)
	p := got.Policies[0]
	if p.ProbeURL != previous.Policies[0].ProbeURL || len(p.ProbeFallbackURLs) != 1 || p.ConnectTimeoutMS != 2500 || p.ProbeIntervalSeconds != 30 || len(p.Routes) != 1 || got.Listeners[0].Password != "new-password" {
		t.Fatal("failed candidate health settings retained or security rollback widened access")
	}
}

func TestRecoveryBlueprintKeepsKnownGoodProbeTargetsForAutomaticReconcile(t *testing.T) {
	previous := Revision{Config: config.Config{Routes: []config.Route{{ID: "kept", Outbound: json.RawMessage(`{}`)}}, Policies: []config.Policy{{ID: "p", Routes: []string{"kept"}, ProbeURL: "https://good.invalid/", ProbeFallbackURLs: []string{"https://reserve.invalid/"}, ProbeIntervalSeconds: 30, ConnectTimeoutMS: 2500}}}}
	policy := catalog.Policy{PoolID: "approved-new-pool", ProbeURL: "https://bad.invalid/", ProbeFallbackURLs: []string{}, ProbeIntervalSeconds: 10, ConnectTimeoutMS: 500}
	body, _ := json.Marshal(policy)
	desired := Revision{Config: config.Config{Routes: []config.Route{{ID: "kept", Outbound: json.RawMessage(`{}`)}}, Policies: []config.Policy{{ID: "p", Routes: []string{"kept"}, ProbeURL: policy.ProbeURL}}}, Blueprint: map[string][]store.Object{"policies": {{Kind: "policies", ID: "p", Data: body}}}}
	recovered := recoveryRevision(&previous, desired)
	got, err := catalog.Decode[catalog.Policy](recovered.Blueprint["policies"][0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProbeURL != previous.Config.Policies[0].ProbeURL || len(got.ProbeFallbackURLs) != 1 || got.ProbeIntervalSeconds != 30 || got.ConnectTimeoutMS != 2500 {
		t.Fatal("recovery blueprint kept the rejected health target for the next automatic source reconciliation")
	}
	if got.PoolID != policy.PoolID || string(desired.Blueprint["policies"][0].Data) != string(body) {
		t.Fatal("recovery changed other approved settings or mutated the failed draft")
	}
}

func TestFirstFailedRolloutPublishesAnEmptyRecoveryBlueprint(t *testing.T) {
	desired := Revision{
		Config: config.Config{
			Managed: true, StateDir: "/state", MetricsListen: "127.0.0.1:0", ProbeURL: "http://localhost/healthz",
			UDP:   config.UDP{Bind: "127.0.0.1", Advertise: "127.0.0.1", PortMin: 31000, PortMax: 31000},
			Cores: []config.Core{{ID: "candidate"}}, Routes: []config.Route{{ID: "new", Core: "candidate"}},
			Policies:  []config.Policy{{ID: "p", Routes: []string{"new"}}},
			Listeners: []config.Listener{{ID: "entry", Protocol: "socks5", Listen: "127.0.0.1:1080", Policy: "p"}},
		},
		Blueprint: map[string][]store.Object{"listeners": {{ID: "entry"}}, "settings": {{Kind: "settings", ID: "global", Data: json.RawMessage(`{}`)}}},
	}
	got := recoveryRevision(nil, desired)
	if !got.Config.Managed || len(got.Config.Routes) != 0 || len(got.Config.Cores) != 0 || len(got.Config.Policies) != 0 || len(got.Config.Listeners) != 0 || len(got.Blueprint) != 1 || len(got.Blueprint["settings"]) != 1 || got.Blueprint["settings"][0].ID != "global" {
		t.Fatal("first failed rollout can remain active or be resurrected by automatic reconciliation")
	}
	if len(desired.Config.Routes) != 1 || len(desired.Config.Policies[0].Routes) != 1 || len(desired.Blueprint) != 2 {
		t.Fatal("failed candidate mutated")
	}
	if err := got.Config.Validate(); err != nil {
		t.Fatalf("empty recovery must remain a valid managed configuration: %v", err)
	}
	// A second failed attempt sees the earlier empty recovery as its prior
	// revision. It must retain the same fence instead of reviving the draft.
	again := recoveryRevision(&got, desired)
	if len(again.Config.Routes) != 0 || len(again.Config.Listeners) != 0 || len(again.Config.Policies) != 0 || len(again.Blueprint) != 1 || len(again.Blueprint["settings"]) != 1 {
		t.Fatal("a repeated failed first rollout revived unapplied routing")
	}
}
