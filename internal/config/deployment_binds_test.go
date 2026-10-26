package config

import "testing"

func TestDeploymentBindPolicyRejectsCachedBridgeAndAcceptsExplicitHost(t *testing.T) {
	t.Setenv("WORKER_ALLOWED_BIND_IPS", "192.0.2.10,100.64.0.10,172.19.0.1")
	t.Setenv("WORKER_METRICS_LISTEN", "172.19.0.1:9090")
	c := Config{MetricsListen: "172.19.0.1:9090", UDP: UDP{Bind: "0.0.0.0", Advertise: "auto"}, Listeners: []Listener{{Listen: "192.0.2.10:18080"}, {Listen: "100.64.0.10:11080"}, {Listen: "172.19.0.1:8083"}}}
	if e := ValidateDeploymentBinds(c); e != nil {
		t.Fatal(e)
	}
	for _, bind := range []string{"0.0.0.0:8083", "[::]:8084", "unknown:18080", "192.0.2.11:11080"} {
		bad := c
		bad.Listeners = []Listener{{Listen: bind}}
		if e := ValidateDeploymentBinds(bad); e == nil {
			t.Fatal("unsafe cached/fresh bind", bind)
		}
	}
	bad := c
	bad.MetricsListen = "0.0.0.0:9090"
	if e := ValidateDeploymentBinds(bad); e == nil {
		t.Fatal("metrics exposed")
	}
	bad = c
	bad.UDP.Advertise = "192.0.2.10"
	if e := ValidateDeploymentBinds(bad); e == nil {
		t.Fatal("wildcard UDP in strict host mode")
	}
	t.Setenv("WORKER_ALLOWED_BIND_IPS", "")
	if e := ValidateDeploymentBinds(bad); e != nil {
		t.Fatal("legacy bridge behavior changed")
	}
}
