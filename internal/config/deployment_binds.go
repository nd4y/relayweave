package config

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
)

// A host worker is admitted only to explicitly provisioned interfaces. This is
// applied to cached and fresh configurations before any listener/core is opened.
func ValidateDeploymentBinds(c Config) error {
	raw := strings.TrimSpace(os.Getenv("WORKER_ALLOWED_BIND_IPS"))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 16 {
		return fmt.Errorf("invalid deployment bind policy")
	}
	allowed := map[netip.Addr]bool{}
	for _, value := range parts {
		ip, e := netip.ParseAddr(strings.TrimSpace(value))
		if e != nil || ip.IsUnspecified() {
			return fmt.Errorf("invalid deployment bind policy")
		}
		allowed[ip.Unmap()] = true
	}
	permitted := func(host string) bool {
		ip, e := netip.ParseAddr(host)
		return e == nil && !ip.IsUnspecified() && (ip.IsLoopback() || allowed[ip.Unmap()])
	}
	for _, listener := range c.Listeners {
		host, _, e := net.SplitHostPort(listener.Listen)
		if e != nil || !permitted(host) {
			return fmt.Errorf("listener bind rejected by deployment policy")
		}
	}
	expected := os.Getenv("WORKER_METRICS_LISTEN")
	host, _, e := net.SplitHostPort(c.MetricsListen)
	if expected == "" || c.MetricsListen != expected || e != nil || !permitted(host) {
		return fmt.Errorf("metrics bind rejected by deployment policy")
	}
	if c.UDP.Advertise != "auto" && !permitted(c.UDP.Bind) {
		return fmt.Errorf("UDP bind rejected by deployment policy")
	}
	return nil
}
