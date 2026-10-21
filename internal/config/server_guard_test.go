package config

import "testing"

func TestPrivateProxyExceptionRequiresNarrowExplicitCIDR(t *testing.T) {
	for _, cidr := range []string{"0.0.0.0/0", "127.0.0.1/32", "10.0.0.0/8", "fd00::/8", "169.254.169.254/32", "invalid"} {
		if ValidateServerCIDRs([]string{cidr}) == nil {
			t.Fatal("broad or metadata exception", cidr)
		}
	}
	for _, cidr := range []string{"172.18.0.3/32", "10.10.0.0/16", "fd01:1:2:3::/64", "100.100.100.1/32"} {
		if err := ValidateServerCIDRs([]string{cidr}); err != nil {
			t.Fatal("narrow exception rejected", cidr, err)
		}
	}
}
