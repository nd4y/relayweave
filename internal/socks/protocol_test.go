package socks

import (
	"bytes"
	"testing"
)

func TestAddressRoundTrip(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "::1", "origin"} {
		a, e := Address(h, 443)
		if e != nil {
			t.Fatal(e)
		}
		host, p, raw, e := ReadAddress(bytes.NewReader(a))
		if e != nil || host != h || p != 443 || !bytes.Equal(a, raw) {
			t.Fatalf("round trip %s: %s %d %v", h, host, p, e)
		}
		for n := 0; n < len(a); n++ {
			if _, _, _, e = ReadAddress(bytes.NewReader(a[:n])); e == nil {
				t.Fatalf("accepted truncated address at %d", n)
			}
		}
	}
}
func TestDatagramRejectsFragmentsAndTruncation(t *testing.T) {
	a, _ := Address("origin", 53)
	b := append([]byte{0, 0, 0}, a...)
	if e := ValidateDatagram(b); e != nil {
		t.Fatal(e)
	}
	b[2] = 1
	if ValidateDatagram(b) == nil {
		t.Fatal("accepted fragmented datagram")
	}
	for _, b := range [][]byte{{}, {0, 0, 0, 3}, {0, 0, 0, 3, 255}, {0, 0, 0, 9}} {
		if ValidateDatagram(b) == nil {
			t.Fatal("accepted malformed datagram")
		}
	}
}
func FuzzDatagram(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 53})
	f.Fuzz(func(t *testing.T, b []byte) { ValidateDatagram(b) })
}
