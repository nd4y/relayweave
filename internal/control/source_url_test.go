package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"proxyplatform/internal/store"
)

func TestSourceURLDisclosureIsNarrowAndUncached(t *testing.T) {
	const raw = `{"url":"https://example.invalid/sub/token?q=secret","headers":{"Authorization":"private-header"}}`
	object := store.Object{Kind: "sources", Data: json.RawMessage(raw)}
	s := &Server{}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			w := httptest.NewRecorder()
			s.revealSourceURL(w, httptest.NewRequest(method, "/", nil), object)
			if w.Header().Get("Cache-Control") != "no-store, private" {
				t.Fatal("disclosure response must not be cached")
			}
			if method != http.MethodPost {
				if w.Code != http.StatusMethodNotAllowed || strings.Contains(w.Body.String(), "secret") {
					t.Fatal("non-POST disclosure accepted or exposed a secret")
				}
				return
			}
			var body map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 200 || len(body) != 1 || body["url"] != "https://example.invalid/sub/token?q=secret" {
				t.Fatal("unexpected disclosure fields")
			}
		})
	}
	redacted := s.redact(object)
	if strings.Contains(string(redacted.Data), "token?q=secret") || strings.Contains(string(redacted.Data), "private-header") {
		t.Fatal("ordinary source response leaked secrets")
	}
}

func TestSourceURLDisclosureRejectsInvalidObject(t *testing.T) {
	for _, object := range []store.Object{{Kind: "listeners", Data: json.RawMessage(`{}`)}, {Kind: "sources", Data: json.RawMessage(`{"url":42}`)}} {
		w := httptest.NewRecorder()
		(&Server{}).revealSourceURL(w, httptest.NewRequest(http.MethodPost, "/", nil), object)
		if w.Code < 400 || strings.Contains(w.Body.String(), "url\":") {
			t.Fatal("invalid object disclosure accepted")
		}
	}
}
