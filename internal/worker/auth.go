package worker

import (
	"crypto/sha256"
	"crypto/subtle"
	"golang.org/x/crypto/bcrypt"
	"net"
	"proxyplatform/internal/config"
	"strings"
	"sync"
	"time"
)

var authCache = struct {
	sync.Mutex
	entries map[[32]byte]time.Time
}{entries: map[[32]byte]time.Time{}}

func hashedAuth(l config.Listener, user, pass string) bool {
	a, b := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(l.Username))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 || len(pass) > 72 {
		return false
	}
	key := sha256.Sum256([]byte(user + "\x00" + l.Password + "\x00" + pass))
	authCache.Lock()
	expires, ok := authCache.entries[key]
	authCache.Unlock()
	if ok && time.Now().Before(expires) {
		return true
	}
	if bcrypt.CompareHashAndPassword([]byte(l.Password), []byte(pass)) != nil {
		return false
	}
	authCache.Lock()
	if len(authCache.entries) >= 1024 {
		for k, t := range authCache.entries {
			if time.Now().After(t) {
				delete(authCache.entries, k)
			}
		}
	}
	if len(authCache.entries) < 1024 {
		authCache.entries[key] = time.Now().Add(10 * time.Second)
	}
	authCache.Unlock()
	return true
}
var metadataIPv6 = net.ParseIP("fd00:ec2::254")

func forbiddenTarget(target string) bool {
	host, _, e := net.SplitHostPort(target)
	if e != nil {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	switch host {
	case "localhost", "control-plane", "database", "frontend", "worker", "prometheus", "grafana", "loki", "metadata.google.internal":
		return true
	}
	address, _, _ := strings.Cut(host, "%")
	ip := net.ParseIP(address)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.Equal(metadataIPv6) || address == "100.100.100.200")
}
