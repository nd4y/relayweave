package control

import (
	"net/http"

	"proxyplatform/internal/catalog"
	"proxyplatform/internal/store"
)

// The session/CSRF middleware protects this explicit, narrow secret disclosure.
// List/detail responses stay redacted; headers and other secrets never enter it.
func (s *Server) revealSourceURL(w http.ResponseWriter, r *http.Request, o store.Object) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		fail(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if o.Kind != "sources" {
		fail(w, http.StatusNotFound, "not_found")
		return
	}
	source, err := catalog.Decode[catalog.Source](o.Data)
	if err != nil {
		fail(w, http.StatusInternalServerError, "source_url_unavailable")
		return
	}
	respond(w, http.StatusOK, map[string]string{"url": source.URL})
}
