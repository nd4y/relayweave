package worker

import (
	"context"
	"crypto/sha256"
	"net"
	"proxyplatform/internal/config"
	"strconv"
)

type listenerScope struct {
	Listener config.Listener
	Peer     string
}
type listenerScopeKey struct{}

func scopedContext(ctx context.Context, l config.Listener, peer string) context.Context {
	return context.WithValue(ctx, listenerScopeKey{}, listenerScope{l, peer})
}

func listenerAuthority(l config.Listener) [32]byte {
	return sha256.Sum256([]byte(l.Username + "\x00" + l.Password + "\x00" + l.Policy + "\x00" + l.Route + "\x00" + l.Protocol + "\x00" + strconv.FormatBool(l.UDP)))
}

func (r *Runtime) sessionScopeAllowedLocked(scope *listenerScope) bool {
	if scope == nil {
		return true
	}
	c := r.config
	if r.constraints != nil {
		c = *r.constraints
	}
	for _, l := range c.Listeners {
		if l.ID != scope.Listener.ID {
			continue
		}
		if listenerAuthority(l) != listenerAuthority(scope.Listener) {
			return false
		}
		if len(l.AllowCIDRs) == 0 {
			return true
		}
		host, _, err := net.SplitHostPort(scope.Peer)
		if err != nil {
			return false
		}
		ip := net.ParseIP(host)
		for _, cidr := range l.AllowCIDRs {
			_, network, err := net.ParseCIDR(cidr)
			if err == nil && network.Contains(ip) {
				return true
			}
		}
		return false
	}
	return false
}
