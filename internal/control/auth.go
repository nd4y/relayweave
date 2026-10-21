package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type sessionKey struct{}
type sessionData struct {
	Hash    string
	CSRF    string
	Expires time.Time
}

func randomToken() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func tokenHash(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	return e == nil && strings.EqualFold(u.Host, r.Host) && (u.Scheme == "http" || u.Scheme == "https")
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		fail(w, 403, "origin_or_content_type")
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	s.mu.Lock()
	now := time.Now()
	v := s.loginRates[ip]
	if v == nil || now.After(v.Until) {
		if len(s.loginRates) > 1024 {
			for k, v := range s.loginRates {
				if now.After(v.Until) {
					delete(s.loginRates, k)
				}
			}
		}
		v = &rate{Until: now.Add(time.Minute)}
		if len(s.loginRates) >= 1024 {
			s.mu.Unlock()
			fail(w, 429, "login_rate_limit")
			return
		}
		s.loginRates[ip] = v
	}
	v.Attempts++
	allowed := v.Attempts <= 10
	s.mu.Unlock()
	if !allowed {
		w.Header().Set("Retry-After", "60")
		fail(w, 429, "login_rate_limit")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Password) > 256 || bcrypt.CompareHashAndPassword(s.PasswordHash, []byte(tokenHash(body.Password))) != nil {
		fail(w, 401, "incorrect_password")
		return
	}
	token, csrf := randomToken(), randomToken()
	expires := time.Now().Add(12 * time.Hour)
	if _, e := s.Store.DB.Exec(r.Context(), `INSERT INTO sessions(token_hash,csrf,expires_at) VALUES($1,$2,$3)`, tokenHash(token), csrf, expires); e != nil {
		fail(w, 503, "session_store_unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "rw_session", Value: token, Path: "/", HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 43200})
	respond(w, 200, map[string]any{"authenticated": true, "csrf": csrf, "expires_at": expires})
}
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := r.Cookie("rw_session")
		if e != nil || len(c.Value) != 64 {
			fail(w, 401, "authentication_required")
			return
		}
		v := sessionData{Hash: tokenHash(c.Value)}
		if e = s.Store.DB.QueryRow(r.Context(), `SELECT csrf,expires_at FROM sessions WHERE token_hash=$1 AND expires_at>now()`, v.Hash).Scan(&v.CSRF, &v.Expires); e != nil {
			fail(w, 401, "session_expired")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			provided := r.Header.Get("X-CSRF-Token")
			if !sameOrigin(r) || subtle.ConstantTimeCompare([]byte(provided), []byte(v.CSRF)) != 1 {
				fail(w, 403, "csrf_rejected")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, v)))
	})
}
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	v := r.Context().Value(sessionKey{}).(sessionData)
	respond(w, 200, map[string]any{"authenticated": true, "csrf": v.CSRF, "expires_at": v.Expires})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	v := r.Context().Value(sessionKey{}).(sessionData)
	s.Store.DB.Exec(r.Context(), `DELETE FROM sessions WHERE token_hash=$1`, v.Hash)
	http.SetCookie(w, &http.Cookie{Name: "rw_session", Path: "/", Value: "", MaxAge: -1, HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteStrictMode})
	respond(w, 200, map[string]bool{"authenticated": false})
}
