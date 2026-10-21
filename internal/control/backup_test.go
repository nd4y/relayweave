package control

import (
	"bytes"
	"testing"
)

func TestBackupEncryption(t *testing.T) {
	plain := []byte("private-subscription-content")
	password := "a sufficiently long test password"
	a, e := encryptBackup(plain, password)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(a, plain) {
		t.Fatal("plaintext leaked")
	}
	got, e := decryptBackup(a, password)
	if e != nil || !bytes.Equal(got, plain) {
		t.Fatal("round trip")
	}
	if _, e = decryptBackup(a, "wrong password"); e == nil {
		t.Fatal("wrong password accepted")
	}
	a[len(a)-1] ^= 1
	if _, e = decryptBackup(a, password); e == nil {
		t.Fatal("tamper accepted")
	}
	for _, bad := range [][]byte{nil, []byte("RWBACKUP1"), make([]byte, 38)} {
		if _, e = decryptBackup(bad, password); e == nil {
			t.Fatal("bad header accepted")
		}
	}
}
