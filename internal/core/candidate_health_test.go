package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"proxyplatform/internal/config"
)

type candidateHealthFixture struct{ directHealthFixture }

func (candidateHealthFixture) Family() string { return "candidate-health-test" }

func TestCandidateChecksFreshTargetWithoutChangingAcceptedHealth(t *testing.T) {
	if _, err := AdapterFor("candidate-health-test"); err != nil {
		if err := RegisterAdapter(candidateHealthFixture{}); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == "/slow" {
			time.Sleep(100 * time.Millisecond)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	i := &Instance{Config: config.Core{Family: "candidate-health-test"}}
	i.ConfigureProbe(config.Config{ProbeURL: server.URL + "/good"})
	accepted := i.probeConfig.Load()
	i.Ready.Store(true)
	candidate := config.Config{ProbeURL: server.URL + "/bad", ConnectTimeoutMS: 500}
	if i.CheckRoute(context.Background(), server.Listener.Addr().String(), candidate) {
		t.Fatal("cached Ready accepted a failed candidate target")
	}
	if !i.Ready.Load() || i.probeConfig.Load() != accepted {
		t.Fatal("failed candidate changed the accepted health settings")
	}
	candidate.ProbeFallbackURLs = []string{server.URL + "/slow"}
	if !i.CheckRoute(context.Background(), server.Listener.Addr().String(), candidate) {
		t.Fatal("candidate fallback or slow healthy target rejected")
	}
	i.ProbeAllowed = func() bool { return false }
	if i.CheckRoute(context.Background(), server.Listener.Addr().String(), candidate) {
		t.Fatal("candidate check bypassed revocation")
	}
}

func TestOldProbeCannotOverwriteCommittedCandidateHealth(t *testing.T) {
	i := &Instance{}
	i.ConfigureProbe(config.Config{ProbeURL: "https://old.invalid/"})
	old := i.probeConfig.Load()
	i.ConfigureValidatedProbe(config.Config{ProbeURL: "https://new.invalid/"})
	i.publishProbe(old, false, time.Second)
	if !i.Ready.Load() || i.ProbeSamples.Load() != 0 {
		t.Fatal("old in-flight probe overwrote the newly validated settings")
	}
	i.publishProbe(i.probeConfig.Load(), false, time.Millisecond)
	if i.Ready.Load() {
		t.Fatal("failure of the current health target was ignored")
	}
}
