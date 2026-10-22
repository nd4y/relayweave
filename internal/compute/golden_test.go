package compute

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The fixture was produced with independent Python canonicalisation and
// Ed25519, so a matching bug in Go Sign and CheckSignature cannot hide it.
func TestIndependentRecommendationGoldenVector(t *testing.T) {
	var vector struct {
		Seed      string         `json:"seed_hex"`
		Public    string         `json:"public_key"`
		Canonical string         `json:"canonical_payload"`
		Hash      string         `json:"payload_hash"`
		Signature string         `json:"signature"`
		Payload   Recommendation `json:"payload"`
	}
	raw, e := os.ReadFile("testdata/recommendation-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &vector); e != nil {
		t.Fatal(e)
	}
	canonical, e := Canonical(vector.Payload)
	if e != nil || string(canonical) != vector.Canonical {
		t.Fatal("canonical mismatch", e)
	}
	h, e := Hash(vector.Payload)
	if e != nil || h != vector.Hash {
		t.Fatal("digest mismatch", h, e)
	}
	seed, e := hex.DecodeString(vector.Seed)
	if e != nil || len(seed) != 32 {
		t.Fatal("fixture seed")
	}
	key := ed25519.NewKeyFromSeed(seed)
	signed, e := Sign(vector.Payload, key)
	if e != nil || signed.Signature != vector.Signature {
		t.Fatal("signature mismatch", e)
	}
	public, e := base64.RawURLEncoding.DecodeString(vector.Public)
	if e != nil {
		t.Fatal(e)
	}
	if e = CheckSignature(signed, public); e != nil {
		t.Fatal(e)
	}
	if e = Verify(context.Background(), signed.Snapshot, signed.Candidate); e != nil {
		t.Fatal("independent candidate range/reference mismatch", e)
	}
}
