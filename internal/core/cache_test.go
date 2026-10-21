package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCacheProtectsRecoveryAndRefusesSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"26.9.30", "26.9.30-test"} {
		if err := os.Mkdir(filepath.Join(root, "v"+v), 0700); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(root, "v"+v, "xray"), []byte("binary"), 0600)
	}
	protected := map[string][]string{"26.9.30": {"previous_good"}}
	if err := RemoveCached(context.Background(), root, "26.9.30", protected); err == nil {
		t.Fatal("deleted recovery artifact")
	}
	if err := RemoveCached(context.Background(), root, "26.10.17", nil); err == nil {
		t.Fatal("accepted builtin removal")
	}
	if err := RemoveCached(context.Background(), root, "../outside", nil); err == nil {
		t.Fatal("accepted traversal")
	}
	if err := os.Symlink(outside, filepath.Join(root, "v26.8.1")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveCached(context.Background(), root, "26.8.1", nil); err == nil {
		t.Fatal("followed symlink")
	}
	if err := RemoveCached(context.Background(), root, "26.9.30-test", protected); err != nil {
		t.Fatal(err)
	}
	status, err := Cache(root, protected)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Items) != 1 || !status.Items[0].Protected || status.Bytes != 6 {
		t.Fatalf("unexpected cache: %+v", status)
	}
	if _, err := os.Stat(filepath.Join(outside, "sentinel")); err != nil {
		t.Fatal(err)
	}
}
