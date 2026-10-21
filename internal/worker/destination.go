package worker

import (
	"crypto/sha256"
	"errors"
	"net"
	"time"
)

func isTimeout(e error) bool { var v net.Error; return errors.As(e, &v) && v.Timeout() }

// Target failures must not quarantine every proxy when a destination itself fails.
// The bounded local cache uses hashes and is never exported as metric labels or logs.
func (g *Gateway) targetCooling(target, route string) bool {
	key := sha256.Sum256([]byte(target))
	g.destMu.Lock()
	defer g.destMu.Unlock()
	if g.destFailures == nil {
		return false
	}
	return time.Now().Before(g.destFailures[key][route])
}
func (g *Gateway) targetFailed(target, route string) {
	key := sha256.Sum256([]byte(target))
	g.destMu.Lock()
	defer g.destMu.Unlock()
	if g.destFailures == nil {
		g.destFailures = map[[32]byte]map[string]time.Time{}
	}
	if len(g.destFailures) >= 1024 {
		for k, m := range g.destFailures {
			active := false
			for _, until := range m {
				if time.Now().Before(until) {
					active = true
					break
				}
			}
			if !active {
				delete(g.destFailures, k)
			}
		}
	}
	m := g.destFailures[key]
	if m == nil {
		if len(g.destFailures) >= 1024 {
			return
		}
		m = map[string]time.Time{}
		g.destFailures[key] = m
	}
	if len(m) < 32 {
		m[route] = time.Now().Add(3 * time.Second)
	}
}
