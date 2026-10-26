package control

import (
	"proxyplatform/internal/catalog"
	"testing"
)

func partialProfile(t *testing.T, host, credential string) catalog.Profile {
	t.Helper()
	parsed, err := catalog.Parse([]byte("vless://" + credential + "@" + host + ":443?security=tls&sni=peer.example"))
	if err != nil {
		t.Fatal(err)
	}
	p := parsed.Profiles[0]
	p.Present = true
	return p
}

func TestPartialCannotReintroduceExcludedGroupAsNewNodes(t *testing.T) {
	uuid := "11111111-1111-1111-1111-111111111111"
	a, b := partialProfile(t, "a.example", uuid), partialProfile(t, "b.example", uuid)
	c, d := partialProfile(t, "c.example", uuid), partialProfile(t, "d.example", uuid)
	e := partialProfile(t, "e.example", "22222222-2222-2222-2222-222222222222")
	protected := map[string]bool{a.Fingerprint: true}
	merged, held, changed := safePartial([]catalog.Profile{a, b}, []catalog.Profile{c, d, e}, protected)
	if !changed || len(merged) != 3 || len(held) != 2 {
		t.Fatal("independent safe addition lost or protected aliases enabled")
	}
	for _, p := range merged {
		if p.Fingerprint == c.Fingerprint || p.Fingerprint == d.Fingerprint {
			t.Fatal("excluded group bypass through new profile ID")
		}
	}
	diff := compareProfilesProtected([]catalog.Profile{a, b}, []catalog.Profile{a, b, c}, protected)
	if !snapshotReview(diff) || diff["ambiguous"] != 1 {
		t.Fatal("ambiguous alias addition not held", diff)
	}
}

func TestFilteredRotationHoldDoesNotBlockIndependentSafeRotation(t *testing.T) {
	a := partialProfile(t, "a.example", "11111111-1111-1111-1111-111111111111")
	b := partialProfile(t, "b.example", "22222222-2222-2222-2222-222222222222")
	c := partialProfile(t, "c.example", "11111111-1111-1111-1111-111111111111")
	d := partialProfile(t, "d.example", "22222222-2222-2222-2222-222222222222")
	merged, held, changed := safePartialHeld([]catalog.Profile{a, b}, []catalog.Profile{c, d}, nil, map[string]bool{c.Fingerprint: true})
	if !changed || len(merged) != 2 || len(held) != 1 {
		t.Fatal("independent safe rotation held")
	}
	found := map[string]bool{}
	for _, p := range merged {
		found[p.Fingerprint] = true
	}
	if !found[a.Fingerprint] || !found[d.Fingerprint] || found[c.Fingerprint] {
		t.Fatal("filtered member replaced")
	}
}
