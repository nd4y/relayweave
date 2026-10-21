package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"proxyplatform/internal/store"
)

type Bundle struct {
	Certificate []byte `json:"certificate"`
	PrivateKey  []byte `json:"private_key"`
}
type Authority struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	PEM  []byte
}

func Open(dir string) (*Authority, error) {
	certPath, keyPath := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "ca.key")
	b, e := os.ReadFile(certPath)
	if e == nil {
		k, e := os.ReadFile(keyPath)
		if e != nil {
			return nil, e
		}
		c, _ := pem.Decode(b)
		kb, _ := pem.Decode(k)
		if c == nil || kb == nil {
			return nil, fmt.Errorf("invalid authority")
		}
		cert, e := x509.ParseCertificate(c.Bytes)
		if e != nil {
			return nil, e
		}
		key, e := x509.ParseECPrivateKey(kb.Bytes)
		if e != nil {
			return nil, e
		}
		pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
		if !ok || !pub.Equal(&key.PublicKey) || !cert.IsCA || time.Now().After(cert.NotAfter) {
			return nil, fmt.Errorf("authority key, certificate or validity mismatch")
		}
		return &Authority{Cert: cert, Key: key, PEM: b}, nil
	}
	if !os.IsNotExist(e) {
		return nil, e
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		return nil, e
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "RelayWeave worker authority"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(3, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		return nil, e
	}
	private, e := x509.MarshalECPrivateKey(key)
	if e != nil {
		return nil, e
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if e = store.AtomicFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: private}), 0600); e != nil {
		return nil, e
	}
	if e = store.AtomicFile(certPath, ca, 0644); e != nil {
		return nil, e
	}
	cert, e = x509.ParseCertificate(der)
	return &Authority{Cert: cert, Key: key, PEM: ca}, e
}
func (a *Authority) Issue(name string, server bool, csrDER []byte) (Bundle, error) {
	var key *ecdsa.PrivateKey
	var public any
	if len(csrDER) > 0 {
		csr, e := x509.ParseCertificateRequest(csrDER)
		if e != nil || csr.CheckSignature() != nil {
			return Bundle{}, fmt.Errorf("invalid CSR")
		}
		public = csr.PublicKey
	} else {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			return Bundle{}, e
		}
		key = k
		public = &k.PublicKey
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-5 * time.Minute), NotAfter: time.Now().Add(90 * 24 * time.Hour), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature}
	if leaf.NotAfter.After(a.Cert.NotAfter) {
		leaf.NotAfter = a.Cert.NotAfter
	}
	if server {
		leaf.DNSNames = []string{name, "control-plane", "localhost"}
		leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	} else {
		leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, e := x509.CreateCertificate(rand.Reader, leaf, a.Cert, public, a.Key)
	if e != nil {
		return Bundle{}, e
	}
	bundle := Bundle{Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
	if key != nil {
		k, e := x509.MarshalECPrivateKey(key)
		if e != nil {
			return bundle, e
		}
		bundle.PrivateKey = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: k})
	}
	return bundle, nil
}

// CertificateExpiries exposes only expiration timestamps, never key material.
func CertificateExpiries(dir string) map[string]int64 {
	out := map[string]int64{}
	for role, name := range map[string]string{"authority": "ca.pem", "server": "server.json", "client": "client.json"} {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(e) {
			continue
		}
		out[role] = 0
		if e != nil || len(b) > 65536 {
			continue
		}
		if name != "ca.pem" {
			var bundle Bundle
			if json.Unmarshal(b, &bundle) != nil {
				continue
			}
			b = bundle.Certificate
		}
		block, _ := pem.Decode(b)
		if block == nil {
			continue
		}
		cert, e := x509.ParseCertificate(block.Bytes)
		if e == nil {
			out[role] = cert.NotAfter.Unix()
		}
	}
	return out
}
func Save(path string, b Bundle) error {
	data, e := json.Marshal(b)
	if e != nil {
		return e
	}
	return store.AtomicFile(path, data, 0600)
}
func Load(path string) (tls.Certificate, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return tls.Certificate{}, e
	}
	var v Bundle
	if e = json.Unmarshal(b, &v); e != nil {
		return tls.Certificate{}, e
	}
	return tls.X509KeyPair(v.Certificate, v.PrivateKey)
}
func Init(controlDir, workerDir string) error {
	a, e := Open(controlDir)
	if e != nil {
		return e
	}
	serverPath := filepath.Join(controlDir, "server.json")
	if !validBundle(serverPath, a, true) {
		b, e := a.Issue("control-plane", true, nil)
		if e != nil {
			return e
		}
		if e = Save(serverPath, b); e != nil {
			return e
		}
	}
	workerPath := filepath.Join(workerDir, "client.json")
	if !validBundle(workerPath, a, false) {
		b, e := a.Issue("worker-1", false, nil)
		if e != nil {
			return e
		}
		if e = Save(workerPath, b); e != nil {
			return e
		}
	}
	return store.AtomicFile(filepath.Join(workerDir, "ca.pem"), a.PEM, 0644)
}
func validBundle(path string, a *Authority, server bool) bool {
	c, e := Load(path)
	if e != nil || len(c.Certificate) == 0 {
		return false
	}
	leaf, e := x509.ParseCertificate(c.Certificate[0])
	if e != nil || time.Until(leaf.NotAfter) < 7*24*time.Hour {
		return false
	}
	roots := x509.NewCertPool()
	roots.AddCert(a.Cert)
	usage := x509.ExtKeyUsageClientAuth
	if server {
		usage = x509.ExtKeyUsageServerAuth
	}
	_, e = leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{usage}})
	return e == nil
}
func RenewServer(dir string) error {
	a, e := Open(dir)
	if e != nil {
		return e
	}
	path := filepath.Join(dir, "server.json")
	if validBundle(path, a, true) {
		return nil
	}
	b, e := a.Issue("control-plane", true, nil)
	if e != nil {
		return e
	}
	return Save(path, b)
}
func RenewalCSR(dir string) ([]byte, error) {
	c, e := Load(filepath.Join(dir, "client.json"))
	if e != nil {
		return nil, e
	}
	leaf, e := x509.ParseCertificate(c.Certificate[0])
	if e != nil || time.Until(leaf.NotAfter) > 14*24*time.Hour {
		return nil, e
	}
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: leaf.Subject}, c.PrivateKey)
}
func AcceptRenewal(dir string, cert []byte) error {
	path := filepath.Join(dir, "client.json")
	data, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var b Bundle
	if e = json.Unmarshal(data, &b); e != nil {
		return e
	}
	if _, e = tls.X509KeyPair(cert, b.PrivateKey); e != nil {
		return e
	}
	b.Certificate = cert
	return Save(path, b)
}
func ServerTLS(dir string) (*tls.Config, error) {
	b, e := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if e != nil {
		return nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("invalid CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		c, e := Load(filepath.Join(dir, "server.json"))
		return &c, e
	}}, nil
}
func ClientTLS(dir string) (*tls.Config, error) {
	b, e := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if e != nil {
		return nil, e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(b) {
		return nil, fmt.Errorf("invalid CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "control-plane", RootCAs: roots, GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		c, e := Load(filepath.Join(dir, "client.json"))
		return &c, e
	}}, nil
}
