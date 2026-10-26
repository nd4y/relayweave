package worker

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const (
	connectDiagnosticsEnv = "WORKER_CONNECT_DIAGNOSTICS_CIDRS"
	connectRouteHeader    = "X-RelayWeave-Route-ID"
	connectProfileHeader  = "X-RelayWeave-Profile-ID"
	connectISPHeader      = "X-RelayWeave-ISP"
	connectEstablished    = "HTTP/1.1 200 Connection Established\r\n\r\n"
	connectIdentifierMax  = 128
	connectDiagnosticsMax = 32
)

// connectDiagnosticPolicy is an immutable opt-in allowlist for CONNECT
// response diagnostics. The zero value is disabled: the default response is
// unchanged unless WORKER_CONNECT_DIAGNOSTICS_CIDRS names at least one valid
// prefix. Any invalid entry disables diagnostics for every peer.
type connectDiagnosticPolicy struct {
	enabled  bool
	prefixes []netip.Prefix
}

func loadConnectDiagnosticPolicy(raw string) connectDiagnosticPolicy {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return connectDiagnosticPolicy{}
	}
	parts := strings.Split(raw, ",")
	if len(parts) > connectDiagnosticsMax {
		return connectDiagnosticPolicy{}
	}
	prefixes := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(part))
		if err != nil {
			return connectDiagnosticPolicy{}
		}
		addr := prefix.Addr()
		if addr.Is4In6() {
			bits := prefix.Bits() - 96
			if bits < 0 {
				return connectDiagnosticPolicy{}
			}
			prefix = netip.PrefixFrom(addr.Unmap(), bits)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return connectDiagnosticPolicy{enabled: true, prefixes: prefixes}
}

// allows uses only the actual peer address. Callers must pass the transport's
// RemoteAddr, never a Forwarded or X-Forwarded-For value.
func (p connectDiagnosticPolicy) allows(remote string) bool {
	if !p.enabled {
		return false
	}
	host, port, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.WithZone("").Unmap()
	for _, prefix := range p.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func validConnectIdentifier(value string) bool {
	if value == "" || len(value) > connectIdentifierMax {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// response returns the exact bytes written for a successful CONNECT. It never
// includes the target, credentials, client identity, or partial diagnostics.
func (p connectDiagnosticPolicy) response(remote string, route *Route) string {
	if route == nil || !p.allows(remote) {
		return connectEstablished
	}
	routeID := route.Config.ID
	profileID := route.Config.Profile()
	isp := route.Config.EgressID
	if !validConnectIdentifier(routeID) || !validConnectIdentifier(profileID) || !validConnectIdentifier(isp) {
		return connectEstablished
	}
	return "HTTP/1.1 200 Connection Established\r\n" +
		connectRouteHeader + ": " + routeID + "\r\n" +
		connectProfileHeader + ": " + profileID + "\r\n" +
		connectISPHeader + ": " + isp + "\r\n\r\n"
}
