package control

import (
	"net/http/httptest"
	"proxyplatform/internal/topology"
	"testing"
)

func TestTopologyEndpointsRequireExistingSession(t *testing.T) {
	s := &Server{Topology: topology.NewHub()}
	for _, path := range []string{"snapshot", "events", "history", "decisions/missing"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/topology/"+path, nil))
		if w.Code != 401 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}
func TestTopologyReadOnlyBoundsAndExpiredIDs(t *testing.T) {
	s := &Server{Topology: topology.NewHub()}
	for _, tc := range []struct {
		method, path string
		parts        []string
		want         int
	}{{"POST", "snapshot", []string{"topology", "snapshot"}, 405}, {"GET", "decisions/missing", []string{"topology", "decisions", "missing"}, 410}, {"GET", "missing", []string{"topology", "missing"}, 404}, {"GET", "snapshot", []string{"topology", "snapshot"}, 200}, {"GET", "history", []string{"topology", "history"}, 200}} {
		w := httptest.NewRecorder()
		s.topologyAPI(w, httptest.NewRequest(tc.method, "/api/v1/topology/"+tc.path, nil), tc.parts)
		if w.Code != tc.want {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
}
