package control

import (
	"context"
	"os"
	"proxyplatform/internal/compute"
)

// Explicit opt-in. No new listener, keys, migration or background job exists
// in an unchanged deployment.
func (s *Server) StartCompute(ctx context.Context) (func(), error) {
	path := os.Getenv("RELAYWEAVE_COMPUTE_CONFIG")
	if path == "" {
		return func() {}, nil
	}
	cfg, e := compute.ReadServiceConfig(path)
	if e != nil {
		return nil, e
	}
	c, stop, e := compute.Start(ctx, s.Store.DB, cfg)
	if e != nil {
		return nil, e
	}
	s.Compute = c
	return stop, nil
}
