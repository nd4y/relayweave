package catalog

import "testing"

func rotationProfile(t *testing.T, host, credential string) Profile {
	t.Helper()
	p, err := Parse([]byte("vless://" + credential + "@" + host + ":443?security=tls&type=tcp&sni=peer.example#node"))
	if err != nil {
		t.Fatal(err)
	}
	v := p.Profiles[0]
	v.Present = true
	return v
}

func TestAddressRotationRequiresExactPeerAndCredential(t *testing.T) {
	a := rotationProfile(t, "old.example", "11111111-1111-1111-1111-111111111111")
	b := rotationProfile(t, "new.example", "11111111-1111-1111-1111-111111111111")
	if AddressMatches([]Profile{a}, []Profile{b})[b.Fingerprint] != a.Fingerprint {
		t.Fatal("address-only rotation lost")
	}
	c := rotationProfile(t, "new.example", "22222222-2222-2222-2222-222222222222")
	if len(AddressMatches([]Profile{a}, []Profile{c})) != 0 {
		t.Fatal("credential change authorized as address rotation")
	}
	b.Outbound = append(b.Outbound[:len(b.Outbound)-1], []byte(`,"unknownSecurityField":true}`)...)
	if len(AddressMatches([]Profile{a}, []Profile{b})) != 0 {
		t.Fatal("unknown effective field discarded")
	}
}

func TestAmbiguousCredentialsDoNotTransferLogicalIdentity(t *testing.T) {
	a := rotationProfile(t, "a.example", "11111111-1111-1111-1111-111111111111")
	b := rotationProfile(t, "b.example", "11111111-1111-1111-1111-111111111111")
	c := rotationProfile(t, "c.example", "11111111-1111-1111-1111-111111111111")
	if len(AddressMatches([]Profile{a, b}, []Profile{c})) != 0 {
		t.Fatal("ambiguous identity transferred")
	}
}

func TestEquivalentUnpinnedGroupRotatesWithoutReview(t *testing.T) {
	a := rotationProfile(t, "a.example", "11111111-1111-1111-1111-111111111111")
	b := rotationProfile(t, "b.example", "11111111-1111-1111-1111-111111111111")
	c := rotationProfile(t, "c.example", "11111111-1111-1111-1111-111111111111")
	d := rotationProfile(t, "d.example", "11111111-1111-1111-1111-111111111111")
	if len(AddressMatches([]Profile{a, b}, []Profile{c, d})) != 2 {
		t.Fatal("equivalent group stalled")
	}
	if len(AddressMatchesProtected([]Profile{a, b}, []Profile{c, d}, map[string]bool{a.Fingerprint: true})) != 0 {
		t.Fatal("manual pin transferred")
	}
}
