package compute

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
)

// StrictDecode rejects duplicate keys before Go's struct decoder can silently
// retain the last value. Only bounded, integer-only protocol JSON is accepted.
func StrictDecode(raw []byte, out any) error {
	if len(raw) > 64<<10 {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := scanJSON(d, 0); err != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return ErrInvalid
	}
	return nil
}

func scanJSON(d *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch x := t.(type) {
	case json.Delim:
		switch x {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] || len(key) > 64 {
					return ErrInvalid
				}
				for _, r := range key {
					if r < 32 || r > 126 {
						return ErrInvalid
					}
				}
				seen[key] = true
				if len(seen) > 64 {
					return ErrInvalid
				}
				if e = scanJSON(d, depth+1); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return ErrInvalid
			}
		case '[':
			count := 0
			for d.More() {
				count++
				if count > 256 {
					return ErrInvalid
				}
				if e := scanJSON(d, depth+1); e != nil {
					return e
				}
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	case json.Number:
		if x.String() == "-0" {
			return ErrInvalid
		}
		n, e := x.Int64()
		if e != nil || n > 1<<53-1 || n < -(1<<53-1) {
			return ErrInvalid
		}
	case string:
		if len(x) > 1024 {
			return ErrInvalid
		}
		for _, r := range x {
			if r < 32 || r > 126 {
				return ErrInvalid
			}
		}
	case nil, bool:
	default:
		return ErrInvalid
	}
	return nil
}

// Canonical is the restricted JCS profile for this ASCII, integer-only schema.
// Maps sort lexically; integer literals are exact; HTML escaping is disabled.
// Generic arbitrary Unicode/floating JSON is deliberately not supported.
func Canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, ErrInvalid
	}
	var value any
	if err = StrictDecode(raw, &value); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err = enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}
func Hash(v any) (string, error) {
	b, e := Canonical(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

type Task struct {
	Version          int      `json:"version"`
	OwnerEpoch       int64    `json:"owner_epoch"`
	ID               string   `json:"id"`
	Attempt          string   `json:"attempt"`
	Session          string   `json:"session"`
	Token            string   `json:"token"`
	Snapshot         Snapshot `json:"snapshot"`
	SnapshotDigest   string   `json:"snapshot_digest"`
	First            uint32   `json:"first"`
	Count            uint32   `json:"count"`
	CPUReservationMS int      `json:"cpu_reservation_ms"`
	WallBudgetMS     int      `json:"wall_budget_ms"`
}

func (t Task) Validate() error {
	if t.Version != 1 || t.OwnerEpoch <= 0 || !opaque(t.ID) || !opaque(t.Attempt) || !opaque(t.Session) || len(t.Token) != 43 || t.CPUReservationMS < 100 || t.CPUReservationMS > 500 || t.WallBudgetMS < 100 || t.WallBudgetMS > 5000 || t.Count == 0 || t.Count > 64 || t.First > 64 {
		return ErrInvalid
	}
	if b, e := base64.RawURLEncoding.DecodeString(t.Token); e != nil || len(b) != 32 {
		return ErrInvalid
	}
	if e := t.Snapshot.Validate(); e != nil {
		return e
	}
	h, e := Hash(t.Snapshot)
	if e != nil || h != t.SnapshotDigest {
		return ErrInvalid
	}
	return nil
}

type Result struct {
	Version        int       `json:"version"`
	OwnerEpoch     int64     `json:"owner_epoch"`
	Task           string    `json:"task"`
	Attempt        string    `json:"attempt"`
	Session        string    `json:"session"`
	SnapshotDigest string    `json:"snapshot_digest"`
	Status         string    `json:"status"`
	Candidate      Candidate `json:"candidate"`
	CPUUsedMS      int64     `json:"cpu_used_ms"`
}

type Recommendation struct {
	Version        int       `json:"version"`
	KeyID          string    `json:"key_id"`
	OwnerEpoch     int64     `json:"owner_epoch"`
	Sequence       int64     `json:"sequence"`
	Snapshot       Snapshot  `json:"snapshot"`
	SnapshotDigest string    `json:"snapshot_digest"`
	Candidate      Candidate `json:"candidate"`
	BlendPPM       uint32    `json:"blend_ppm"`
	Signature      string    `json:"signature,omitempty"`
}

const domain = "relayweave/compute/recommendation/v1\x00"

func signatureBytes(r Recommendation) ([]byte, error) {
	r.Signature = ""
	b, e := Canonical(r)
	if e != nil {
		return nil, e
	}
	return append([]byte(domain), b...), nil
}
func Sign(r Recommendation, key ed25519.PrivateKey) (Recommendation, error) {
	if len(key) != ed25519.PrivateKeySize || !opaque(r.KeyID) || r.Version != 1 || r.OwnerEpoch < 1 || r.Sequence < 1 || r.BlendPPM > 250000 {
		return r, ErrInvalid
	}
	b, e := signatureBytes(r)
	if e != nil {
		return r, e
	}
	r.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, b))
	return r, nil
}
func CheckSignature(r Recommendation, key ed25519.PublicKey) error {
	if r.Version != 1 || r.OwnerEpoch < 1 || r.Sequence < 1 || r.BlendPPM > 250000 || len(key) != ed25519.PublicKeySize {
		return ErrInvalid
	}
	b, e := signatureBytes(r)
	if e != nil {
		return e
	}
	sig, e := base64.RawURLEncoding.DecodeString(r.Signature)
	if e != nil || !ed25519.Verify(key, b, sig) {
		return ErrInvalid
	}
	return nil
}
