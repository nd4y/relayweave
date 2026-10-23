package store

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func TestObjectEncryptionBindsIdentity(t *testing.T) {
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	s := &Store{AEAD: aead}
	data := []byte(`{"password":"test-secret"}`)
	a, e := s.Seal("sources", "a", data)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := s.Seal("sources", "a", data)
	if bytes.Equal(a, b) || bytes.Contains(a, data) {
		t.Fatal("encryption not randomized")
	}
	out, e := s.Open("sources", "a", a)
	if e != nil || !bytes.Equal(out, data) {
		t.Fatal("roundtrip")
	}
	if _, e = s.Open("sources", "b", a); e == nil {
		t.Fatal("identity substitution")
	}
	if _, e = s.Open("profiles", "a", a); e == nil {
		t.Fatal("kind substitution")
	}
	a[len(a)-1] ^= 1
	if _, e = s.Open("sources", "a", a); e == nil {
		t.Fatal("tamper accepted")
	}
}
