package compute

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"time"

	"proxyplatform/internal/identity"
	"proxyplatform/internal/store"
)

const domainMarker = "relayweave-compute-v1\n"

// Bootstrap creates a new trust domain only in an empty directory. It never
// adopts a data-plane CA or replaces an existing identity/key.
func Bootstrap(dir string) error {
	if dir == "" {
		return ErrInvalid
	}
	if entries, e := os.ReadDir(dir); e == nil && len(entries) != 0 {
		return ErrConflict
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	a, e := identity.Open(dir)
	if e != nil {
		return e
	}
	server, e := a.IssueCompute("compute-dispatcher", true, nil)
	if e != nil {
		return e
	}
	if e = identity.Save(filepath.Join(dir, "server.json"), server); e != nil {
		return e
	}
	for _, name := range []string{"signing.seed", "token.key"} {
		key := make([]byte, 32)
		if _, e = rand.Read(key); e != nil {
			return e
		}
		if e = store.AtomicFile(filepath.Join(dir, name), key, 0600); e != nil {
			return e
		}
	}
	if e = store.AtomicFile(filepath.Join(dir, "compute-domain"), []byte(domainMarker), 0600); e != nil {
		return e
	}
	return ExportPublicKeys(dir, filepath.Join(dir, "recommendation-keys.json"))
}

func computeAuthority(dir string) (*identity.Authority, error) {
	marker, e := os.ReadFile(filepath.Join(dir, "compute-domain"))
	if e != nil || string(marker) != domainMarker {
		return nil, ErrInvalid
	}
	return identity.Open(dir)
}

func ExportPublicKeys(dir, path string) error {
	seed, e := ReadKey(filepath.Join(dir, "signing.seed"))
	if e != nil {
		return e
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	raw, e := Canonical(map[string][]byte{tokenHash(string(pub))[:16]: pub})
	if e != nil {
		return e
	}
	return store.AtomicFile(path, raw, 0644)
}

// Provision writes only the peer key/certificate and CA public certificate.
// Administrative approval pins the SPKI, so another CA-signed key with the
// same CN is not sufficient to impersonate this peer.
func Provision(ctx context.Context, r *Repository, authorityDir, peerDir, id string) error {
	if !opaque(id) || peerDir == "" || filepath.Clean(authorityDir) == filepath.Clean(peerDir) {
		return ErrInvalid
	}
	if entries, e := os.ReadDir(peerDir); e == nil && len(entries) != 0 {
		return ErrConflict
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	a, e := computeAuthority(authorityDir)
	if e != nil {
		return e
	}
	bundle, e := a.IssueCompute(id, false, nil)
	if e != nil {
		return e
	}
	block, _ := pem.Decode(bundle.Certificate)
	if block == nil {
		return ErrInvalid
	}
	cert, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return e
	}
	public, e := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if e != nil {
		return e
	}
	if e = identity.Save(filepath.Join(peerDir, "client.json"), bundle); e != nil {
		return e
	}
	if e = store.AtomicFile(filepath.Join(peerDir, "ca.pem"), a.PEM, 0644); e != nil {
		return e
	}
	return r.ApproveCertificate(ctx, id, tokenHash(string(public)))
}

func renewServer(dir string) error {
	a, e := computeAuthority(dir)
	if e != nil {
		return e
	}
	path := filepath.Join(dir, "server.json")
	cert, e := identity.Load(path)
	if e == nil && len(cert.Certificate) > 0 {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err == nil && time.Until(leaf.NotAfter) > 48*time.Hour {
			return nil
		}
	}
	b, e := a.IssueCompute("compute-dispatcher", true, nil)
	if e != nil {
		return e
	}
	return identity.Save(path, b)
}

func (c *Client) RenewIdentity(ctx context.Context, dir string) error {
	path := filepath.Join(dir, "client.json")
	data, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var bundle identity.Bundle
	if json.Unmarshal(data, &bundle) != nil {
		return ErrInvalid
	}
	cert, e := identity.Load(path)
	if e != nil || len(cert.Certificate) == 0 {
		return ErrInvalid
	}
	leaf, e := x509.ParseCertificate(cert.Certificate[0])
	if e != nil {
		return e
	}
	if time.Until(leaf.NotAfter) > 48*time.Hour {
		return nil
	}
	csr, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: leaf.Subject}, cert.PrivateKey)
	if e != nil {
		return e
	}
	var reply struct {
		Certificate []byte `json:"certificate"`
	}
	if _, e = c.post(ctx, "/compute/v1/renew", "", struct {
		CSR []byte `json:"csr"`
	}{csr}, &reply); e != nil {
		return e
	}
	block, _ := pem.Decode(reply.Certificate)
	if block == nil {
		return ErrInvalid
	}
	renewed, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return e
	}
	rootPEM, e := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if e != nil {
		return e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootPEM) {
		return ErrInvalid
	}
	if _, e = renewed.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); e != nil {
		return e
	}
	if renewed.Subject.CommonName != leaf.Subject.CommonName || !renewed.NotAfter.After(leaf.NotAfter) || renewed.NotAfter.Sub(renewed.NotBefore) > 7*24*time.Hour+10*time.Minute {
		return ErrInvalid
	}
	if _, e = tls.X509KeyPair(reply.Certificate, bundle.PrivateKey); e != nil {
		return e
	}
	bundle.Certificate = reply.Certificate
	if e = saveRenewedBundle(path, bundle); e != nil {
		return e
	}
	c.HTTP.CloseIdleConnections()
	return nil
}
