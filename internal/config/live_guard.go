package config

import (
	"encoding/hex"
	"fmt"
	"time"
)

// LiveGuard is an independent admission fence. It can remove permission from
// accepted routes, but cannot create listeners, routes or upstream credentials.
type LiveGuard struct {
	Version int64                  `json:"version"`
	Routes  map[string]RoutePermit `json:"routes"`
}

type RoutePermit struct {
	ServerPolicyDigest string           `json:"server_policy_digest,omitempty"`
	EstablishedOnly    bool             `json:"established_only,omitempty"`
	Previous           []RevisionPermit `json:"previous,omitempty"`
	Fingerprint        string           `json:"fingerprint"`
	ValidUntil         time.Time        `json:"valid_until"`
}

type RevisionPermit struct {
	Fingerprint string    `json:"fingerprint"`
	ValidUntil  time.Time `json:"valid_until"`
}

func (p RoutePermit) Permits(fp string, now time.Time, overlap bool) bool {
	if p.Fingerprint == fp && now.Before(p.ValidUntil) {
		return true
	}
	if overlap {
		for _, old := range p.Previous {
			if old.Fingerprint == fp && now.Before(old.ValidUntil) && now.Before(p.ValidUntil) {
				return true
			}
		}
	}
	return false
}

func (g LiveGuard) Validate() error {
	if g.Version < 1 || len(g.Routes) > 20000 {
		return fmt.Errorf("live_guard_invalid")
	}
	for route, permit := range g.Routes {
		if len(permit.Previous) > 2 {
			return fmt.Errorf("live_guard_invalid")
		}
		for _, old := range permit.Previous {
			b, err := hex.DecodeString(old.Fingerprint)
			if err != nil || (len(b) != 16 && len(b) != 32) || old.ValidUntil.IsZero() {
				return fmt.Errorf("live_guard_invalid")
			}
		}
		b, err := hex.DecodeString(permit.Fingerprint)
		// Catalogue fingerprints are the existing 128-bit SHA-256 identities;
		// explicit route fingerprints may also use the full 256-bit digest.
		if !id.MatchString(route) || err != nil || (len(b) != 16 && len(b) != 32) || permit.ValidUntil.IsZero() {
			return fmt.Errorf("live_guard_invalid")
		}
	}
	return nil
}

func (g LiveGuard) Clone() LiveGuard {
	out := LiveGuard{Version: g.Version, Routes: make(map[string]RoutePermit, len(g.Routes))}
	for id, permit := range g.Routes {
		permit.Previous = append([]RevisionPermit(nil), permit.Previous...)
		out.Routes[id] = permit
	}
	return out
}

func (g LiveGuard) Legacy() LiveGuard {
	out := g.Clone()
	for id, permit := range out.Routes {
		if permit.EstablishedOnly {
			delete(out.Routes, id)
		} else {
			permit.Previous = nil
			out.Routes[id] = permit
		}
	}
	return out
}
