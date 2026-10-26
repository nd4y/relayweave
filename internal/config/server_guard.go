package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
)

func ValidateServerCIDRs(cidrs []string) error {
	if len(cidrs) > 16 {
		return fmt.Errorf("proxy_CIDR_limit")
	}
	for _, text := range cidrs {
		p, err := netip.ParsePrefix(text)
		private := p.Addr().IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(p.Addr())
		if err != nil || !private || p.Addr().IsLoopback() || (p.Addr().Is4() && p.Bits() < 16) || (p.Addr().Is6() && p.Bits() < 64) {
			return fmt.Errorf("proxy_CIDR_invalid")
		}
	}
	return nil
}

func ServerPolicyDigest(checked, allow bool, cidrs []string) string {
	normalized := append([]string(nil), cidrs...)
	sort.Strings(normalized)
	b, _ := json.Marshal([]any{checked, allow, normalized})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
