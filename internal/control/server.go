package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
	"proxyplatform/internal/identity"
	"proxyplatform/internal/store"
	"proxyplatform/internal/topology"
)

type Server struct {
	Topology     *topology.Hub
	Store        *store.Store
	PasswordHash []byte
	DataDir      string
	IdentityDir  string
	Authority    *identity.Authority
	CookieSecure bool
	ctx          context.Context
	mu           sync.Mutex
	loginRates   map[string]*rate
	changed      chan struct{}
	jobs         chan struct{}
	catalogueMu  sync.Mutex
	catalogue    *decodedCatalogue
	guardMu      sync.Mutex
	guardCache   *config.LiveGuard
}
type rate struct {
	Attempts int
	Until    time.Time
}
type envelope struct {
	Name    string          `json:"name"`
	Data    json.RawMessage `json:"data"`
	Version int64           `json:"version,omitempty"`
}

func New(ctx context.Context) (*Server, error) {
	password := os.Getenv("ADMIN_PASSWORD")
	if len(password) < 12 || len(password) > 256 {
		return nil, fmt.Errorf("ADMIN_PASSWORD must contain 12..256 bytes")
	}
	dir := os.Getenv("DATA_DIR")
	if dir == "" {
		dir = "/data"
	}
	pki := os.Getenv("IDENTITY_DIR")
	if pki == "" {
		pki = "/control-identity"
	}
	s, e := store.New(ctx, os.Getenv("DATABASE_URL"), filepath.Join(dir, "master.key"))
	if e != nil {
		return nil, e
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(tokenHash(password)), bcrypt.DefaultCost)
	if e != nil {
		s.DB.Close()
		return nil, e
	}
	a, e := identity.Open(pki)
	if e != nil {
		s.DB.Close()
		return nil, e
	}
	server := &Server{Store: s, PasswordHash: hash, DataDir: dir, IdentityDir: pki, Authority: a, CookieSecure: os.Getenv("COOKIE_SECURE") == "true", ctx: ctx, loginRates: map[string]*rate{}, changed: make(chan struct{}), jobs: make(chan struct{}, 1)}
	// A password rotation must invalidate sessions created under the previous environment.
	server.Topology = topology.NewHub()
	fingerprint := sha256.Sum256([]byte(password))
	epoch := hex.EncodeToString(fingerprint[:])
	old, err := s.Get(ctx, "internal", "auth-epoch")
	if err == nil {
		var saved struct {
			Hash string `json:"hash"`
		}
		json.Unmarshal(old.Data, &saved)
		if saved.Hash != epoch {
			s.DB.Exec(ctx, `DELETE FROM sessions`)
			data, _ := json.Marshal(map[string]string{"hash": epoch})
			old.Data = data
			if _, e = s.Put(ctx, old, old.Version); e != nil {
				return nil, e
			}
		}
	} else if errors.Is(err, store.ErrNotFound) {
		data, _ := json.Marshal(map[string]string{"hash": epoch})
		if _, e = s.Put(ctx, store.Object{Kind: "internal", ID: "auth-epoch", Name: "auth epoch", Data: data}, 0); e != nil && !errors.Is(e, store.ErrConflict) {
			return nil, e
		}
	} else {
		return nil, err
	}
	settings := catalog.Settings{DefaultCore: "26.10.17", UDPAdvertise: "127.0.0.1", UDPPortMin: 31000, UDPPortMax: 31255, OfflineLeaseSeconds: 86400, DownloadMbps: 800, UploadMbps: 500, ProxyBind: "0.0.0.0"}
	if ip := net.ParseIP(os.Getenv("INITIAL_PROXY_ADVERTISE_IP")); ip != nil {
		settings.UDPAdvertise = ip.String()
	}
	if _, e = s.Get(ctx, "settings", "global"); errors.Is(e, store.ErrNotFound) {
		b, _ := json.Marshal(settings)
		_, e = s.Put(ctx, store.Object{Kind: "settings", ID: "global", Name: "Настройки", Data: b}, 0)
		if e != nil && !errors.Is(e, store.ErrConflict) {
			return nil, e
		}
	}
	// Build the encrypted catalogue's read model before accepting requests. After
	// a cold restart readiness includes this work; the first administrator does
	// not pay the full decode cost on an interactive request.
	started := time.Now()
	bootstrap, done := context.WithTimeout(ctx, 60*time.Second)
	catalogue, err := server.profileCatalogue(bootstrap)
	done()
	if err != nil {
		s.DB.Close()
		return nil, fmt.Errorf("catalogue_bootstrap_unavailable")
	}
	slog.Info("catalogue read model ready", "profiles", len(catalogue.items), "milliseconds", time.Since(started).Milliseconds())
	go server.jobLoop()
	go server.maintenance()
	return server, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if s.Store.DB.Ping(ctx) != nil {
			fail(w, 503, "database_unavailable")
			return
		}
		respond(w, 200, map[string]any{"status": "ok", "name": "RelayWeave", "version": "0.1.0-dev"})
	})
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.Handle("GET /api/v1/auth/session", s.auth(http.HandlerFunc(s.session)))
	mux.Handle("POST /api/v1/auth/logout", s.auth(http.HandlerFunc(s.logout)))
	mux.Handle("/api/v1/", s.auth(http.HandlerFunc(s.api)))
	mux.HandleFunc("GET /client-sub/", s.clientSubscription)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/events/stream") && r.URL.Path != "/api/v1/topology/events" {
			timeout := 15 * time.Second
			if strings.HasPrefix(r.URL.Path, "/api/v1/backup") || strings.HasPrefix(r.URL.Path, "/api/v1/support") {
				timeout = 5 * time.Minute
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			r = r.WithContext(ctx)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Frame-Options", "DENY")
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("API panic recovered")
				fail(w, 500, "internal_error")
			}
		}()
		mux.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]any{"error": map[string]string{"code": code}})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	if d.Decode(v) != nil {
		fail(w, 400, "invalid_json")
		return false
	}
	if d.Decode(new(any)) != io.EOF {
		fail(w, 400, "trailing_json")
		return false
	}
	return true
}
func (s *Server) notify() {
	s.mu.Lock()
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
}
func (s *Server) settings(ctx context.Context) (catalog.Settings, error) {
	o, e := s.Store.Get(ctx, "settings", "global")
	if e != nil {
		return catalog.Settings{}, e
	}
	return catalog.Decode[catalog.Settings](o.Data)
}
func (s *Server) maintenance() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.Topology.Expire()
			s.expireAccess(s.ctx)
			s.Store.DB.Exec(s.ctx, `DELETE FROM sessions WHERE expires_at<now(); DELETE FROM events WHERE time<now()-interval '30 days'`)
			s.Store.DB.Exec(s.ctx, `DELETE FROM objects WHERE (kind='trash' AND updated_at<now()-interval '24 hours') OR (kind IN ('operations','worker-results','worker-commands') AND queue_state NOT IN ('queued','running') AND updated_at<now()-interval '30 days')`)
			objects, _, e := s.Store.List(s.ctx, "sources", 1000, 0)
			if e == nil {
				for _, o := range objects {
					v, e := catalog.Decode[catalog.Source](o.Data)
					if e == nil && v.Enabled && v.IntervalSeconds > 0 && (v.NextRefresh.IsZero() || time.Now().After(v.NextRefresh)) {
						s.enqueue(s.ctx, "refresh", o.ID, nil, "scheduled-"+v.NextRefresh.UTC().Format(time.RFC3339Nano))
					}
				}
			}
			if e := identity.RenewServer(s.IdentityDir); e != nil {
				slog.Error("server certificate renewal failed")
			}
		}
	}
}
func version(r *http.Request) int64 {
	v, _ := strconv.ParseInt(strings.Trim(r.Header.Get("If-Match"), "\""), 10, 64)
	return v
}
