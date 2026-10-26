package control

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Client exports contain only a connection to an explicitly published RelayWeave
// listener. Provider profiles, source URLs and administrator credentials never enter them.
type accessGrant struct {
	Fingerprint string    `json:"fingerprint,omitempty"`
	Listener    string    `json:"listener"`
	Host        string    `json:"host"`
	Password    string    `json:"password,omitempty"`
	Expires     time.Time `json:"expires"`
}
type accessInput struct {
	Listener string `json:"listener"`
	Host     string `json:"host"`
	Password string `json:"password"`
	Hours    int    `json:"hours"`
	Confirm  bool   `json:"confirm_secrets"`
}
type accessHost struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	HTTPOnly bool   `json:"http_only,omitempty"`
	Port     int    `json:"port,omitempty"`
}

var accessDNS = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9.-]{0,251}[a-zA-Z0-9])?$`)

func cleanAccessHost(raw string) string {
	host := strings.Trim(strings.TrimSpace(raw), "[]")
	if len(host) > 253 || strings.ContainsAny(host, "\r\n\t /?#@") {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	if accessDNS.MatchString(host) && !strings.Contains(host, "..") {
		return strings.ToLower(host)
	}
	return ""
}
func accessDefaultBind() string {
	value := os.Getenv("CLIENT_DEFAULT_BIND_IP")
	if value == "" {
		return "0.0.0.0"
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return ""
}
func accessDefaultPort(protocol string) int {
	key, fallback := "CLIENT_DEFAULT_HTTP_PORT", 18080
	if protocol == "socks5" {
		key, fallback = "CLIENT_DEFAULT_SOCKS_PORT", 11080
	}
	if value, e := strconv.Atoi(os.Getenv(key)); e == nil && value > 0 && value <= 65535 {
		return value
	}
	return fallback
}
func defaultAccessListener(protocol string) catalog.Listener {
	return catalog.Listener{Enabled: true, Listen: net.JoinHostPort(accessDefaultBind(), strconv.Itoa(accessDefaultPort(protocol))), Protocol: protocol, UDP: protocol == "socks5"}
}
func accessPortAllowed(port int) bool {
	raw := os.Getenv("CLIENT_PUBLIC_PORTS")
	if raw == "" {
		raw = "18080,18081,11080,11081"
	}
	for _, part := range strings.Split(raw, ",") {
		n, e := strconv.Atoi(strings.TrimSpace(part))
		if e == nil && n == port {
			return true
		}
	}
	return false
}
func accessListener(l catalog.Listener) (int, bool) {
	_, raw, e := net.SplitHostPort(l.Listen)
	port, pe := strconv.Atoi(raw)
	return port, e == nil && pe == nil && l.Enabled && (l.Protocol == "http" || l.Protocol == "socks5") && accessPortAllowed(port)
}
func (s *Server) accessHosts(ctx context.Context) []accessHost {
	result := []accessHost{}
	for _, item := range strings.Split(os.Getenv("CLIENT_ACCESS_HOSTS"), ",") {
		name, raw, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if host := cleanAccessHost(raw); host != "" && len(name) > 0 && len(name) <= 48 && len(result) < 8 {
			result = append(result, accessHost{Name: name, Host: host})
		}
	}
	if len(result) == 0 {
		if settings, e := s.settings(ctx); e == nil {
			if host := cleanAccessHost(settings.UDPAdvertise); host != "" {
				result = append(result, accessHost{Name: "LAN", Host: host})
			}
		}
	}
	if host := cleanAccessHost(os.Getenv("CLIENT_HTTPS_PROXY_HOST")); host != "" && os.Getenv("CLIENT_HTTPS_LISTENER_ID") != "" {
		port := 443
		if value, e := strconv.Atoi(os.Getenv("CLIENT_HTTPS_PROXY_PORT")); e == nil && value > 0 && value <= 65535 {
			port = value
		}
		result = append(result, accessHost{Name: "HTTPS proxy", Host: host, HTTPOnly: true, Port: port})
	}
	return result
}
func (s *Server) accessHostAllowed(ctx context.Context, host string) bool {
	for _, entry := range s.accessHosts(ctx) {
		if entry.Host == host {
			return true
		}
	}
	return false
}
func (s *Server) accessCompatible(ctx context.Context, id string, l catalog.Listener, host string) bool {
	for _, entry := range s.accessHosts(ctx) {
		if entry.Host != host {
			continue
		}
		if entry.HTTPOnly {
			return l.Protocol == "http" && id == os.Getenv("CLIENT_HTTPS_LISTENER_ID")
		}
		bind, _, e := net.SplitHostPort(l.Listen)
		if e != nil {
			return false
		}
		ip := net.ParseIP(bind)
		target := net.ParseIP(host)
		if ip == nil {
			return false
		}
		if target == nil {
			return ip.IsUnspecified()
		}
		if ip.IsUnspecified() {
			return (ip.To4() != nil) == (target.To4() != nil)
		}
		return ip.Equal(target)
	}
	return false
}
func accessPasswordOK(l catalog.Listener, password string) bool {
	if l.Username == "" && l.Password == "" {
		return password == ""
	}
	if len(password) == 0 || len(password) > 72 {
		return false
	}
	if strings.HasPrefix(l.Password, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(l.Password), []byte(password)) == nil
	}
	return subtle.ConstantTimeCompare([]byte(l.Password), []byte(password)) == 1
}
func accessDocument(name, host string, l catalog.Listener, password string) map[string]any {
	_, raw, _ := net.SplitHostPort(l.Listen)
	port, _ := strconv.Atoi(raw)
	scheme, kind := "http", "http"
	if l.Protocol == "socks5" {
		scheme, kind = "socks5", "socks"
	}
	tlsEnabled := l.Protocol == "http" && host == cleanAccessHost(os.Getenv("CLIENT_HTTPS_PROXY_HOST")) && os.Getenv("CLIENT_HTTPS_LISTENER_ID") != ""
	if tlsEnabled {
		scheme = "https"
		port = 443
		if value, e := strconv.Atoi(os.Getenv("CLIENT_HTTPS_PROXY_PORT")); e == nil && value > 0 && value <= 65535 {
			port = value
		}
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(host, strconv.Itoa(port)), Fragment: "RelayWeave " + name}
	outbound := map[string]any{"type": kind, "tag": "RelayWeave " + name, "server": host, "server_port": port}
	if tlsEnabled {
		outbound["tls"] = map[string]any{"enabled": true, "server_name": host}
	}
	if kind == "socks" {
		outbound["version"] = "5"
	}
	if l.Username != "" {
		u.User = url.UserPassword(l.Username, password)
		outbound["username"], outbound["password"] = l.Username, password
	}
	native, _ := json.Marshal(outbound)
	config := map[string]any{"outbounds": []any{outbound}}
	b, _ := json.MarshalIndent(config, "", "  ")
	return map[string]any{"uri": u.String(), "throne_uri": "throne://add/" + base64.RawURLEncoding.EncodeToString(native), "config": string(b), "contains_credentials": l.Username != ""}
}

// Export only a configuration acknowledged active by the worker, never a draft.
func (s *Server) accessApplied(ctx context.Context, id string, current catalog.Listener) bool {
	pointer, e := s.Store.Get(ctx, "internal", "desired")
	if e != nil {
		return false
	}
	object, e := s.Store.Get(ctx, "revisions", pointer.Name)
	if e != nil {
		return false
	}
	revision, e := catalog.Decode[Revision](object.Data)
	if e != nil || revision.Status != "active" {
		return false
	}
	for _, object := range revision.Blueprint["listeners"] {
		if object.ID == id {
			listener, e := catalog.Decode[catalog.Listener](object.Data)
			return e == nil && reflect.DeepEqual(listener, current)
		}
	}
	return false
}
func (s *Server) accessValidate(ctx context.Context, g accessGrant) (store.Object, catalog.Listener, error) {
	o, e := s.Store.Get(ctx, "listeners", g.Listener)
	if e != nil {
		return o, catalog.Listener{}, e
	}
	l, e := catalog.Decode[catalog.Listener](o.Data)
	_, allowed := accessListener(l)
	if e != nil || !allowed || (g.Fingerprint != "" && g.Fingerprint != tokenHash(string(o.Data))) || !s.accessCompatible(ctx, g.Listener, l, g.Host) || !accessPasswordOK(l, g.Password) {
		return o, l, errors.New("export_listener_changed_or_credentials_invalid")
	}
	return o, l, nil
}
func (s *Server) expireAccess(ctx context.Context) {
	rows, _, e := s.Store.List(ctx, "client-exports", 32, 0)
	if e != nil {
		return
	}
	for _, o := range rows {
		g, e := catalog.Decode[accessGrant](o.Data)
		if e != nil || !time.Now().Before(g.Expires) {
			_ = s.Store.Delete(ctx, o.Kind, o.ID, o.Version)
		}
	}
}
func (s *Server) clientAccessAPI(w http.ResponseWriter, r *http.Request, parts []string) {
	w.Header().Set("Cache-Control", "no-store")
	if len(parts) == 1 && r.Method == "GET" {
		s.expireAccess(r.Context())
		rows, _, e := s.Store.List(r.Context(), "listeners", 32, 0)
		if e != nil {
			fail(w, 503, "client_access_unavailable")
			return
		}
		listeners := []map[string]any{}
		for _, o := range rows {
			l, e := catalog.Decode[catalog.Listener](o.Data)
			port, ok := accessListener(l)
			if e == nil && ok && s.accessApplied(r.Context(), o.ID, l) {
				hosts := []string{}
				for _, entry := range s.accessHosts(r.Context()) {
					if s.accessCompatible(r.Context(), o.ID, l, entry.Host) {
						hosts = append(hosts, entry.Host)
					}
				}
				if len(hosts) > 0 {
					listeners = append(listeners, map[string]any{"id": o.ID, "name": o.Name, "protocol": l.Protocol, "port": port, "username": l.Username, "authentication": l.Username != "", "hosts": hosts})
				}
			}
		}
		grants, _, e := s.Store.List(r.Context(), "client-exports", 32, 0)
		if e != nil {
			fail(w, 503, "client_access_unavailable")
			return
		}
		exports := []map[string]any{}
		for _, o := range grants {
			if g, e := catalog.Decode[accessGrant](o.Data); e == nil {
				exports = append(exports, map[string]any{"id": o.ID, "listener": g.Listener, "host": g.Host, "expires": g.Expires})
			}
		}
		respond(w, 200, map[string]any{"hosts": s.accessHosts(r.Context()), "listeners": listeners, "subscriptions": exports, "formats": []string{"http_uri", "socks5_uri", "throne_add", "sing_box_json"}, "token_hours_max": 2160, "default_bind": accessDefaultBind(), "default_ports": map[string]int{"http": accessDefaultPort("http"), "socks5": accessDefaultPort("socks5")}})
		return
	}
	if len(parts) == 2 && r.Method == "DELETE" && len(parts[1]) == 64 {
		o, e := s.Store.Get(r.Context(), "client-exports", parts[1])
		if e != nil {
			fail(w, 404, "not_found")
			return
		}
		if e = s.Store.Delete(r.Context(), o.Kind, o.ID, o.Version); e != nil {
			fail(w, 409, "revoke_conflict")
			return
		}
		respond(w, 200, map[string]any{"revoked": true})
		return
	}
	if len(parts) != 2 || r.Method != "POST" || (parts[1] != "render" && parts[1] != "subscriptions") {
		fail(w, 405, "method_not_allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var in accessInput
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || len(in.Listener) > 48 || len(in.Host) > 253 || len(in.Password) > 72 {
		fail(w, 400, "invalid_client_access")
		return
	}
	host := cleanAccessHost(in.Host)
	if host == "" || !s.accessHostAllowed(r.Context(), host) {
		fail(w, 400, "unconfigured_access_host")
		return
	}
	grant := accessGrant{Listener: in.Listener, Host: host, Password: in.Password}
	o, l, e := s.accessValidate(r.Context(), grant)
	if e != nil {
		fail(w, 400, "export_listener_changed_or_credentials_invalid")
		return
	}
	if !s.accessApplied(r.Context(), o.ID, l) {
		fail(w, 409, "apply_listener_first")
		return
	}
	if (l.Username != "" || parts[1] == "subscriptions") && !in.Confirm {
		fail(w, 400, "confirm_secret_export")
		return
	}
	if parts[1] == "render" {
		respond(w, 200, accessDocument(o.Name, host, l, in.Password))
		return
	}
	if in.Hours < 1 || in.Hours > 2160 {
		fail(w, 400, "invalid_subscription_expiry")
		return
	}
	s.expireAccess(r.Context())
	grant.Fingerprint = tokenHash(string(o.Data))
	grant.Expires = time.Now().Add(time.Duration(in.Hours) * time.Hour).UTC()
	token := randomToken()
	b, _ := json.Marshal(grant)
	record, e := s.Store.Put(r.Context(), store.Object{ID: tokenHash(token), Kind: "client-exports", Name: "RelayWeave client subscription", Data: b}, 0)
	if e != nil {
		if errors.Is(e, store.ErrLimit) {
			fail(w, 409, "client_export_limit")
		} else {
			fail(w, 503, "client_export_unavailable")
		}
		return
	}
	respond(w, 201, map[string]any{"id": record.ID, "path": "/client-sub/" + token, "expires": grant.Expires, "scope": "relayweave_listener_only"})
}
func (s *Server) clientSubscription(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/client-sub/")
	if len(token) != 64 || strings.ContainsAny(token, "/?#") {
		fail(w, 404, "not_found")
		return
	}
	o, e := s.Store.Get(r.Context(), "client-exports", tokenHash(token))
	if e != nil {
		fail(w, 404, "not_found")
		return
	}
	grant, e := catalog.Decode[accessGrant](o.Data)
	if e != nil || grant.Fingerprint == "" || !time.Now().Before(grant.Expires) {
		fail(w, 410, "subscription_expired_or_changed")
		return
	}
	listener, l, e := s.accessValidate(r.Context(), grant)
	if e != nil {
		fail(w, 410, "subscription_expired_or_changed")
		return
	}
	if !s.accessApplied(r.Context(), listener.ID, l) {
		fail(w, 503, "listener_rollout_pending")
		return
	}
	document := accessDocument(listener.Name, grant.Host, l, grant.Password)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="relayweave-client.json"`)
	if r.Method != "HEAD" {
		_, _ = w.Write([]byte(document["config"].(string)))
	}
}
