package worker

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
)

// Host networking must not turn worker diagnostics into a LAN service.
// Bind is explicit; this optional allowlist additionally constrains peers.
func metricsAccess(next http.Handler) (http.Handler, error) {
	raw := strings.TrimSpace(os.Getenv("WORKER_METRICS_ALLOWED_CIDRS"))
	if raw == "" {
		return next, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 16 {
		return nil, fmt.Errorf("invalid metrics allowlist")
	}
	prefixes := []netip.Prefix{}
	for _, part := range parts {
		prefix, e := netip.ParsePrefix(strings.TrimSpace(part))
		if e != nil {
			return nil, fmt.Errorf("invalid metrics allowlist")
		}
		prefixes = append(prefixes, prefix)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, e := net.SplitHostPort(r.RemoteAddr)
		address, pe := netip.ParseAddr(host)
		allowed := e == nil && pe == nil && address.IsLoopback()
		if e == nil && pe == nil {
			for _, prefix := range prefixes {
				if prefix.Contains(address.Unmap()) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}
