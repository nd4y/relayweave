package identity

import (
	"crypto/x509"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitializationAndReenrollment(t *testing.T) {
	dir := t.TempDir()
	control := filepath.Join(dir, "control")
	worker := filepath.Join(dir, "worker")
	if e := Init(control, worker); e != nil {
		t.Fatal(e)
	}
	old, e := os.ReadFile(filepath.Join(worker, "client.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = Init(control, worker); e != nil {
		t.Fatal(e)
	}
	same, _ := os.ReadFile(filepath.Join(worker, "client.json"))
	if string(old) != string(same) {
		t.Fatal("idempotency")
	}
	c, e := Load(filepath.Join(worker, "client.json"))
	if e != nil {
		t.Fatal(e)
	}
	leaf, e := x509.ParseCertificate(c.Certificate[0])
	if e != nil || leaf.Subject.CommonName != "worker-1" || time.Until(leaf.NotAfter) < 89*24*time.Hour {
		t.Fatal("leaf invalid")
	}
	other, e := Open(filepath.Join(dir, "other"))
	if e != nil {
		t.Fatal(e)
	}
	bundle, _ := other.Issue("worker-1", false, nil)
	b, _ := json.Marshal(bundle)
	os.WriteFile(filepath.Join(worker, "client.json"), b, 0600)
	if e = Init(control, worker); e != nil {
		t.Fatal(e)
	}
	ca, e := Open(control)
	if e != nil || !validBundle(filepath.Join(worker, "client.json"), ca, false) {
		t.Fatal("mismatched identity not repaired")
	}
}
