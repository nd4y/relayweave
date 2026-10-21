package topology

import (
	"net/netip"
	"strings"
)

func bounded(s string, max int) bool { return len(s) <= max && !strings.ContainsAny(s, "\r\n\x00") }
func validFrame(f Frame) bool {
	if f.CatalogSize < 0 || f.CatalogSize > 10000 {
		return false
	}
	for _, r := range f.Routes {
		if !bounded(r.BuildSHA256, 64) {
			return false
		}
		if !bounded(r.ID, 48) || !bounded(r.Engine, 80) || !bounded(r.Core, 48) || !bounded(r.Version, 80) || !bounded(r.Subscription, 48) || !bounded(r.ServerHost, 253) || !bounded(r.Group, 48) || !bounded(r.State, 48) || len(r.ServerIPs) > 32 {
			return false
		}
		for _, ip := range r.ServerIPs {
			if _, err := netip.ParseAddr(ip); err != nil {
				return false
			}
		}
	}
	for _, l := range f.Listeners {
		if !bounded(l.ID, 48) || !bounded(l.Policy, 48) || !bounded(l.Protocol, 16) {
			return false
		}
	}
	for _, d := range f.Decisions {
		if !bounded(d.Worker, 48) || !bounded(d.Epoch, 32) {
			return false
		}
		if !bounded(d.Policy, 48) || !bounded(d.Mode, 32) || !bounded(d.Method, 48) || !bounded(d.Listener, 48) || !bounded(d.Protocol, 32) || !bounded(d.Selected, 48) || !bounded(d.Reason, 64) {
			return false
		}
		for _, c := range d.Candidates {
			if !bounded(c.Route, 48) || !bounded(c.Engine, 80) || !bounded(c.Reason, 64) || !bounded(c.Group, 48) || !bounded(c.SpeedSource, 48) || !bounded(c.Formula, 48) || len(c.Factors) > 4 {
				return false
			}
			for _, factor := range c.Factors {
				if !bounded(factor, 64) {
					return false
				}
			}
		}
	}
	return true
}
