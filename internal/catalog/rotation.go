package catalog

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/netip"
	"proxyplatform/internal/config"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RevisionPermit grants a bounded overlap, never a fresh authorization for an
// old credential. The immutable configuration remains in the worker engine.
type RevisionPermit = config.RevisionPermit

var addressTemplateKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("address template key unavailable")
	}
	return key
}()

// AddressTemplate removes only the protocol's one upstream socket address.
// TLS/REALITY, credentials, transport, unknown fields and nested addresses all
// remain significant. A template is private data and must never be exported.
func AddressTemplate(p Profile) string {
	if !p.Supported || len(p.Outbound) == 0 || p.Port < 1 || p.Port > 65535 {
		return ""
	}
	var ob map[string]any
	if json.Unmarshal(p.Outbound, &ob) != nil || insecureConfig(ob) {
		return ""
	}
	protocol, _ := ob["protocol"].(string)
	key := "servers"
	switch protocol {
	case "vless", "vmess":
		key = "vnext"
	case "trojan", "shadowsocks", "socks", "http":
	default:
		return ""
	}
	settings, ok := ob["settings"].(map[string]any)
	if !ok {
		return ""
	}
	servers, ok := settings[key].([]any)
	if !ok || len(servers) != 1 {
		return ""
	}
	server, ok := servers[0].(map[string]any)
	if !ok {
		return ""
	}
	host, ok := server["address"].(string)
	if !ok || canonicalHost(host) != canonicalHost(p.Host) {
		return ""
	}
	port, ok := server["port"].(float64)
	if !ok || port != float64(p.Port) {
		return ""
	}
	delete(server, "address")
	delete(server, "port")
	b, _ := json.Marshal([]any{p.Protocol, p.Transport, p.Security, p.UDP, ob})
	digest := hmac.New(sha256.New, addressTemplateKey)
	digest.Write(b)
	return hex.EncodeToString(digest.Sum(nil))
}

func canonicalHost(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().String()
	}
	return host
}

func insecureConfig(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			if (k == "allowInsecure" || k == "insecure") && value == true {
				return true
			}
			if insecureConfig(value) {
				return true
			}
		}
	case []any:
		for _, value := range x {
			if insecureConfig(value) {
				return true
			}
		}
	}
	return false
}

// AddressMatches returns next fingerprint -> previous fingerprint only for a
// unique two-sided equivalence. Names, countries and provider ordering have no
// authority to transfer a manually pinned identity.
func AddressMatches(previous, next []Profile) map[string]string {
	return AddressMatchesProtected(previous, next, nil)
}

func AddressMatchesProtected(previous, next []Profile, protected map[string]bool) map[string]string {
	old, fresh := map[string][]Profile{}, map[string][]Profile{}
	seen := map[string]bool{}
	for _, p := range previous {
		if p.Present {
			if key := AddressTemplate(p); key != "" {
				old[key] = append(old[key], p)
			}
		}
	}
	for _, p := range next {
		if seen[p.Fingerprint] {
			continue
		}
		seen[p.Fingerprint] = true
		if key := AddressTemplate(p); key != "" {
			fresh[key] = append(fresh[key], p)
		}
	}
	out := map[string]string{}
	for key, profiles := range fresh {
		if len(profiles) > 1 && len(profiles) == len(old[key]) {
			// Equivalent unpinned groups can rotate as a set. Exact members
			// retain identity first; only the remaining equivalent slots map.
			blocked := false
			for _, p := range old[key] {
				blocked = blocked || !p.Enabled || p.CoreOverride || protected[p.Fingerprint]
			}
			if blocked {
				continue
			}
			known := map[string]bool{}
			for _, p := range profiles {
				known[p.Fingerprint] = true
			}
			remainingOld, remainingNew := []Profile{}, []Profile{}
			oldKnown := map[string]bool{}
			for _, p := range old[key] {
				oldKnown[p.Fingerprint] = true
				if !known[p.Fingerprint] {
					remainingOld = append(remainingOld, p)
				}
			}
			for _, p := range profiles {
				if !oldKnown[p.Fingerprint] {
					remainingNew = append(remainingNew, p)
				}
			}
			if len(remainingOld) != len(remainingNew) {
				continue
			}
			sort.Slice(remainingOld, func(i, j int) bool { return remainingOld[i].Fingerprint < remainingOld[j].Fingerprint })
			sort.Slice(remainingNew, func(i, j int) bool { return remainingNew[i].Fingerprint < remainingNew[j].Fingerprint })
			for i, p := range remainingNew {
				out[p.Fingerprint] = remainingOld[i].Fingerprint
			}
			continue
		}
		if len(profiles) != 1 || len(old[key]) != 1 {
			continue
		}
		a, b := old[key][0], profiles[0]
		if a.Fingerprint != b.Fingerprint && net.JoinHostPort(canonicalHost(a.Host), strconv.Itoa(a.Port)) != net.JoinHostPort(canonicalHost(b.Host), strconv.Itoa(b.Port)) {
			out[b.Fingerprint] = a.Fingerprint
		}
	}
	return out
}

// SourceUntil is also used to freeze the old revision's authorization at the
// observation which actually contained it. A new snapshot cannot renew it.
func SourceUntil(s Source) time.Time {
	until := s.LastSuccess.Add(time.Duration(s.MaxStaleSeconds) * time.Second)
	if expiry := s.Quota["expire"]; expiry > 0 && time.Unix(expiry, 0).Before(until) {
		until = time.Unix(expiry, 0).UTC()
	}
	return until
}

func ProfileUntil(s Source, p Profile) time.Time {
	until := SourceUntil(s)
	if !p.FrozenUntil.IsZero() && p.FrozenUntil.Before(until) {
		until = p.FrozenUntil
	}
	return until
}
