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
	Fingerprint string    `json:"fingerprint"`
	ValidUntil  time.Time `json:"valid_until"`
}

func (g LiveGuard) Validate() error {
	if g.Version < 1 || len(g.Routes) > 20000 {
		return fmt.Errorf("live_guard_invalid")
	}
	for route, permit := range g.Routes {
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
		out.Routes[id] = permit
	}
	return out
}
