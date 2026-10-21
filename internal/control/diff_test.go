package control

import (
	"proxyplatform/internal/catalog"
	"testing"
)

func TestSourceDiffCanonicalRotationHistory(t *testing.T) {
	old := []catalog.Profile{{Identity: "host", Fingerprint: "old", Present: true, Supported: true}}
	next := []catalog.Profile{{Identity: "host", Fingerprint: "new", Supported: true}, {Identity: "host", Fingerprint: "new", Supported: true}}
	d := compareProfiles(old, next)
	if d["credentials_changed"] != 1 || d["removed"] != 0 || d["after_total"] != 1 || snapshotReview(d) {
		t.Fatal(d)
	}
	old = append(old, catalog.Profile{Identity: "host", Fingerprint: "historic", Present: false, Supported: true})
	d = compareProfiles(old, next)
	if d["ambiguous"] != 1 || !snapshotReview(d) || d["before_total"] != 1 {
		t.Fatal(d)
	}
	d = compareProfiles(old, []catalog.Profile{{Identity: "host", Fingerprint: "historic", Supported: true}})
	if d["restored"] != 1 || d["removed"] != 1 {
		t.Fatal(d)
	}
}

func TestSourceDiffEmptyAndSupportedLossNeedsReview(t *testing.T) {
	old := []catalog.Profile{{Identity: "a", Fingerprint: "a", Present: true, Supported: true}, {Identity: "b", Fingerprint: "b", Present: true, Supported: true}, {Identity: "c", Fingerprint: "c", Present: true, Supported: true}}
	if !snapshotReview(compareProfiles(old, nil)) {
		t.Fatal("empty result accepted silently")
	}
	next := append([]catalog.Profile{}, old...)
	for i := range next {
		next[i].Supported = false
	}
	if !snapshotReview(compareProfiles(old, next)) {
		t.Fatal("supported profile loss accepted silently")
	}
}

func TestUniqueEndpointTransportChangeAndFullReplacementNeedReview(t *testing.T) {
	old := []catalog.Profile{{Identity: "tls", Fingerprint: "old", Host: "node.example", Port: 443, Present: true, Supported: true}}
	next := []catalog.Profile{{Identity: "reality", Fingerprint: "new", Host: "node.example", Port: 443, Supported: true}}
	d := compareProfiles(old, next)
	if d["parameters_changed"] != 1 || d["added"] != 0 || d["removed"] != 0 || !snapshotReview(d) {
		t.Fatal(d)
	}
	next[0].Host = "replacement.example"
	d = compareProfiles(old, next)
	if d["added"] != 1 || d["removed"] != 1 || !snapshotReview(d) {
		t.Fatal("full catalogue replacement accepted silently", d)
	}
	d = compareProfiles(nil, next)
	if snapshotReview(d) {
		t.Fatal("first import treated as a replacement")
	}
}
