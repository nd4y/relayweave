package control

import (
	"proxyplatform/internal/catalog"
	"testing"
)

func TestAddressRotationDoesNotImplicitlyRevokeHostFilteredSessions(t *testing.T) {
	old := catalog.Profile{Host: "old.example", SourceID: "s", Enabled: true, Supported: true}
	next := old
	next.Host = "new.example"
	filter := catalog.Filter{Conditions: []catalog.Condition{{Field: "host", Operator: "equals", Value: "old.example"}}}
	if !rotationChangesMembership(catalog.Source{Filter: filter}, "p", old, next, nil) {
		t.Fatal("source host filter silently interrupted old sessions")
	}
	pool := catalog.Pool{Enabled: true, Automatic: true, Filter: filter}
	if !rotationChangesMembership(catalog.Source{}, "p", old, next, []catalog.Pool{pool}) {
		t.Fatal("pool membership change ignored")
	}
	if rotationChangesMembership(catalog.Source{}, "p", old, next, []catalog.Pool{{Enabled: true, Automatic: true}}) {
		t.Fatal("ordinary automatic rotation requires review")
	}
	pool.Automatic = false
	pool.SourceIDs = []string{"unrelated"}
	if rotationChangesMembership(catalog.Source{}, "p", old, next, []catalog.Pool{pool}) {
		t.Fatal("unrelated pool blocked safe rotation")
	}
}
