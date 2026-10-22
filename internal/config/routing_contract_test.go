package config

import (
	"testing"
	"time"
)

func TestMixedRoutingContractsPreserveOnlySupportedAuthority(t *testing.T) {
	now := time.Now().UTC()
	oldFP, newFP := "11111111111111111111111111111111", "22222222222222222222222222222222"
	frozen := now.Add(time.Minute)
	guard := LiveGuard{Version: 7, Routes: map[string]RoutePermit{"p": {Fingerprint: newFP, ValidUntil: now.Add(time.Hour), Previous: []RevisionPermit{{Fingerprint: oldFP, ValidUntil: frozen}}}}}
	v1 := guard.ForRoutingVersion(1)
	if len(v1.Routes["p"].Previous) != 0 || v1.Routes["p"].Permits(oldFP, now, true) {
		t.Fatal("v1 received unsupported revision authority")
	}
	v2 := guard.ForRoutingVersion(2)
	if !v2.Routes["p"].EstablishedOnly || !v2.Routes["p"].Permits(oldFP, now, true) || v2.Routes["p"].Permits(oldFP, frozen.Add(time.Nanosecond), true) {
		t.Fatal("v2 continuation was revoked or extended")
	}
	v3 := guard.ForRoutingVersion(3)
	if v3.Routes["p"].EstablishedOnly || len(v3.Routes["p"].Previous) != 1 || !v3.Routes["p"].Permits(newFP, now, false) {
		t.Fatal("v3 current revision lost authority")
	}
	if guard.Routes["p"].EstablishedOnly {
		t.Fatal("projection mutated current guard")
	}
	v2.Routes["p"] = RoutePermit{}
	if !guard.Routes["p"].Permits(oldFP, now, true) {
		t.Fatal("projection shared mutable route map")
	}
}
