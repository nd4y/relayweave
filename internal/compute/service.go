package compute

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"proxyplatform/internal/identity"
)

type ServiceConfig struct {
	Version         int    `json:"version"`
	Enabled         bool   `json:"enabled"`
	Installation    string `json:"installation"`
	Listen          string `json:"listen"`
	IdentityDir     string `json:"identity_dir"`
	SigningSeedFile string `json:"signing_seed_file"`
	TokenKeyFile    string `json:"token_key_file"`
}

func ReadServiceConfig(path string) (ServiceConfig, error) {
	var c ServiceConfig
	raw, e := os.ReadFile(path)
	if e != nil || len(raw) > 8<<10 {
		return c, ErrInvalid
	}
	// Paths are local operator configuration, not remotely supplied protocol.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&c) != nil || dec.Decode(new(any)) != io.EOF || c.Version != 1 {
		return c, ErrInvalid
	}
	if !c.Enabled {
		return c, nil
	}
	host, _, e := net.SplitHostPort(c.Listen)
	ip := net.ParseIP(host)
	addr, _ := netip.AddrFromSlice(ip)
	vpn := addr.IsValid() && netip.MustParsePrefix("100.64.0.0/10").Contains(addr.Unmap())
	if e != nil || ip == nil || !(ip.IsLoopback() || ip.IsPrivate() || vpn) || !opaque(c.Installation) || c.IdentityDir == "" || c.SigningSeedFile == "" || c.TokenKeyFile == "" {
		return c, ErrInvalid
	}
	return c, nil
}

func ReadKey(path string) ([]byte, error) {
	info, e := os.Stat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, ErrInvalid
	}
	key, e := os.ReadFile(path)
	if e != nil || len(key) != 32 {
		return nil, ErrInvalid
	}
	return key, nil
}

func Start(ctx context.Context, db *pgxpool.Pool, cfg ServiceConfig) (*Controller, func(), error) {
	stop := func() {}
	if !cfg.Enabled {
		return nil, stop, nil
	}
	seed, e := ReadKey(cfg.SigningSeedFile)
	if e != nil {
		return nil, stop, e
	}
	token, e := ReadKey(cfg.TokenKeyFile)
	if e != nil {
		return nil, stop, e
	}
	key := ed25519.NewKeyFromSeed(seed)
	keyID := tokenHash(string(key.Public().(ed25519.PublicKey)))[:16]
	a, e := computeAuthority(cfg.IdentityDir)
	if e != nil {
		return nil, stop, e
	}
	if e = renewServer(cfg.IdentityDir); e != nil {
		return nil, stop, e
	}
	c := &Controller{Repo: &Repository{DB: db, Installation: cfg.Installation, Owner: newID(), TokenKey: token}, Key: key, KeyID: keyID}
	c.Renew = func(id string, csr []byte) ([]byte, error) {
		bundle, e := a.IssueCompute(id, false, csr)
		return bundle.Certificate, e
	}
	lastMaintenance := time.Now()
	c.Maintain = func() error {
		if time.Since(lastMaintenance) < time.Minute {
			return nil
		}
		lastMaintenance = time.Now()
		call, done := context.WithTimeout(ctx, time.Second)
		defer done()
		if e := c.Repo.Cleanup(call); e != nil {
			return e
		}
		return renewServer(cfg.IdentityDir)
	}
	setup, finish := context.WithTimeout(ctx, 3*time.Second)
	e = c.Repo.Migrate(setup)
	finish()
	if e != nil {
		return nil, stop, e
	}
	secure, e := identity.ServerTLS(cfg.IdentityDir)
	if e != nil {
		return nil, stop, e
	}
	listener, e := net.Listen("tcp", cfg.Listen)
	if e != nil {
		return nil, stop, e
	}
	child, cancel := context.WithCancel(ctx)
	server := &http.Server{Handler: c, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10, TLSConfig: secure}
	go c.Run(child)
	go func() { _ = server.Serve(tls.NewListener(listener, secure)); c.Epoch.Store(0); cancel() }()
	stop = func() { cancel(); _ = server.Close() }
	return c, stop, nil
}
