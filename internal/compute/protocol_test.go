package compute

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

type protocolDecodeNested struct {
	N int `json:"n"`
}

type protocolDecodeTarget struct {
	A      int64                `json:"a"`
	B      string               `json:"b"`
	Nested protocolDecodeNested `json:"nested"`
}

func TestStrictDecodeRejectsInvalidJSON(t *testing.T) {
	nested := strings.Repeat("[", 10) + "0" + strings.Repeat("]", 10)
	cases := []struct {
		name string
		raw  string
	}{
		{"duplicate key", `{"a":1,"a":2}`},
		{"duplicate nested key", `{"a":1,"b":"x","nested":{"n":1,"n":2}}`},
		{"float", `{"a":1.5}`},
		{"float exponent", `{"a":1e3}`},
		{"unsafe integer", `{"a":9007199254740992}`},
		{"unsafe negative integer", `{"a":-9007199254740992}`},
		{"trailing value", `{"a":1,"b":"x"} {"a":2}`},
		{"trailing scalar", `{"a":1,"b":"x"} 0`},
		{"unknown field", `{"a":1,"b":"x","extra":1}`},
		{"over depth", nested},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out protocolDecodeTarget
			if err := StrictDecode([]byte(tc.raw), &out); !errors.Is(err, ErrInvalid) {
				t.Fatalf("StrictDecode(%q) = %v, want ErrInvalid", tc.raw, err)
			}
		})
	}
}

func TestStrictDecodeAcceptsBoundedProtocolJSON(t *testing.T) {
	var out protocolDecodeTarget
	if err := StrictDecode([]byte(`{"a":1,"b":"x"}`), &out); err != nil {
		t.Fatal(err)
	}
	if out.A != 1 || out.B != "x" {
		t.Fatalf("decoded %+v", out)
	}
	out = protocolDecodeTarget{}
	if err := StrictDecode([]byte("{\"a\":9007199254740991,\"b\":\"x\"}\n \t"), &out); err != nil {
		t.Fatal(err)
	}
	if out.A != 9007199254740991 {
		t.Fatalf("decoded a=%d", out.A)
	}
}

func TestCanonicalDeterministicOrder(t *testing.T) {
	type unordered struct {
		Z int    `json:"z"`
		A int    `json:"a"`
		M string `json:"m"`
	}
	v := unordered{Z: 3, A: 1, M: "x"}
	first, err := Canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"a":1,"m":"x","z":3}`
	if got := string(first); got != want {
		t.Fatalf("Canonical = %s, want %s", got, want)
	}
	second, err := Canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(first) {
		t.Fatalf("Canonical not deterministic: %s != %s", first, second)
	}

	// Map input with deliberately non-sorted insertion order must produce the
	// same lexical ordering as the struct form.
	m, err := Canonical(map[string]any{"z": 3, "a": 1, "m": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(m) != want {
		t.Fatalf("Canonical map = %s, want %s", m, want)
	}

	// Integers up to the safe 2^53-1 bound stay exact.
	big, err := Canonical(struct {
		N int64 `json:"n"`
	}{N: 9007199254740991})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(big); got != `{"n":9007199254740991}` {
		t.Fatalf("Canonical big integer = %s", got)
	}
}

func testSnapshot(t *testing.T) Snapshot {
	t.Helper()
	set, _ := Hash([]string{"a", "b"})
	return Snapshot{
		Version:      1,
		Installation: "inst",
		Executor:     "worker",
		Policy:       "policy",
		Boot:         "boot",
		Sequence:     1,
		Generation:   1,
		RouteSet:     set,
		Objective:    "objective-v1",
		DemandBPS:    1000,
		Groups: []Group{
			{
				ID:          "a",
				Lane:        "lane",
				Baseline:    400000,
				RTTMS:       10,
				Samples:     20,
				CapacityBPS: 1000000,
				BusyBPS:     100000,
			},
			{
				ID:          "b",
				Lane:        "lane",
				Baseline:    600000,
				RTTMS:       20,
				Samples:     20,
				CapacityBPS: 1000000,
				BusyBPS:     100000,
			},
		},
	}
}

func testRecommendation(t *testing.T) Recommendation {
	t.Helper()
	snap := testSnapshot(t)
	if err := snap.Validate(); err != nil {
		t.Fatal(err)
	}
	digest, err := Hash(snap)
	if err != nil {
		t.Fatal(err)
	}
	return Recommendation{
		Version:        1,
		KeyID:          "test-key",
		OwnerEpoch:     1,
		Sequence:       1,
		Snapshot:       snap,
		SnapshotDigest: digest,
		Candidate:      Candidate{ID: 0, Weights: []uint32{400000, 600000}},
		BlendPPM:       1000,
	}
}

func testPrivateKey(seed byte) ed25519.PrivateKey {
	key := make([]byte, ed25519.SeedSize)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return ed25519.NewKeyFromSeed(key)
}

func TestSignCheckSignature(t *testing.T) {
	priv := testPrivateKey(1)
	pub := priv.Public().(ed25519.PublicKey)

	rec, err := Sign(testRecommendation(t), priv)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Signature == "" {
		t.Fatal("empty signature")
	}
	if err := CheckSignature(rec, pub); err != nil {
		t.Fatalf("CheckSignature valid = %v", err)
	}

	tampered := []struct {
		name   string
		change func(*Recommendation)
	}{
		{"blend", func(r *Recommendation) { r.BlendPPM++ }},
		{"sequence", func(r *Recommendation) { r.Sequence++ }},
		{"candidate", func(r *Recommendation) { r.Candidate.ID++ }},
		{"snapshot digest", func(r *Recommendation) { r.SnapshotDigest = strings.Repeat("b", 64) }},
	}
	for _, tc := range tampered {
		t.Run(tc.name, func(t *testing.T) {
			bad := rec
			tc.change(&bad)
			if err := CheckSignature(bad, pub); !errors.Is(err, ErrInvalid) {
				t.Fatalf("CheckSignature tampered = %v, want ErrInvalid", err)
			}
		})
	}

	bad := rec
	bad.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	if err := CheckSignature(bad, pub); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CheckSignature zero signature = %v, want ErrInvalid", err)
	}

	other := testPrivateKey(33).Public().(ed25519.PublicKey)
	if err := CheckSignature(rec, other); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CheckSignature wrong key = %v, want ErrInvalid", err)
	}
}
