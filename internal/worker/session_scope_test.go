package worker

import (
	"proxyplatform/internal/config"
	"testing"
)

func TestContinuationCannotBypassListenerACLOrCredentialRotation(t *testing.T) {
	r := &Runtime{}
	l := config.Listener{ID: "listener", Policy: "p", Protocol: "socks5", Password: "old", AllowCIDRs: []string{"192.168.1.0/24"}}
	scope := &listenerScope{Listener: l, Peer: "192.168.1.23:1234"}
	r.config.Listeners = []config.Listener{l}
	if !r.sessionScopeAllowedLocked(scope) {
		t.Fatal("authorized session rejected")
	}
	restricted := l
	restricted.AllowCIDRs = []string{"192.168.1.24/32"}
	r.constraints = &config.Config{Listeners: []config.Listener{restricted}}
	if r.sessionScopeAllowedLocked(scope) {
		t.Fatal("existing session bypasses narrowed ACL")
	}
	restricted = l
	restricted.Password = "new"
	r.constraints.Listeners = []config.Listener{restricted}
	if r.sessionScopeAllowedLocked(scope) {
		t.Fatal("old listener credentials resurrected")
	}
}

func TestProxySecurityPolicyChangeCannotReuseOldEnginePermission(t *testing.T) {
	r, old, next := rolloverFixture()
	old.Config.CheckServer = true
	old.Config.AllowPrivate = true
	old.Config.ServerAllowCIDRs = []string{"172.18.0.2/32"}
	next.Config.CheckServer = true
	next.Config.AllowPrivate = false
	c := r.config
	c.Routes = []config.Route{next.Config}
	r.Restrict(c)
	if r.overlapAllowedLocked("p", old.Config) {
		t.Fatal("old private destination exception restored by rollover")
	}
}
