package core

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestArchiveInspectionChecksDigestArchitectureAndDuplicate(t *testing.T) {
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1})
	header[18] = 62
	fixture := func(duplicate bool) (string, string) {
		p := filepath.Join(t.TempDir(), "core.zip")
		f, e := os.Create(p)
		if e != nil {
			t.Fatal(e)
		}
		z := zip.NewWriter(f)
		w, _ := z.Create("xray")
		w.Write(header)
		if duplicate {
			w, _ = z.Create("xray")
			w.Write(header)
		}
		if e = z.Close(); e != nil {
			t.Fatal(e)
		}
		f.Close()
		sum, _ := FileHash(p)
		return p, sum
	}
	path, sum := fixture(false)
	digest, e := InspectArchive(path, sum, "amd64")
	if e != nil {
		t.Fatal(e)
	}
	wanted := sha256.Sum256(header)
	if digest != hex.EncodeToString(wanted[:]) {
		t.Fatal("binary digest mismatch")
	}
	if _, e = InspectArchive(path, sum, "arm64"); e == nil {
		t.Fatal("wrong architecture accepted")
	}
	if _, e = InspectArchive(path, hex.EncodeToString(wanted[:])); e == nil {
		t.Fatal("wrong archive digest accepted")
	}
	path, sum = fixture(true)
	if _, e = InspectArchive(path, sum); e == nil {
		t.Fatal("duplicate binary accepted")
	}
}
