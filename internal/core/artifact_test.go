package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCachedArtifactPermissionsAndIntegrity(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "v42")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	bin := []byte("fixture binary")
	sum := sha256.Sum256(bin)
	digest := hex.EncodeToString(sum[:])
	archiveHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	os.WriteFile(filepath.Join(dir, "xray"), bin, 0755)
	m, _ := json.Marshal(map[string]string{"archive_sha256": archiveHash, "binary_sha256": digest})
	os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0644)
	lock, _ := json.Marshal(map[string]any{"artifacts": []Artifact{{Version: "42", Arch: runtime.GOARCH, URL: "https://github.com/XTLS/Xray-core/releases/download/v42/Xray-linux-64.zip", SHA256: archiveHash, BinarySHA256: digest}}})
	path := filepath.Join(root, "lock.json")
	os.WriteFile(path, lock, 0600)
	if e := Install(context.Background(), path, "42", root); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(dir)
	if e != nil {
		t.Fatal(e)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("artifact directory not accessible to runtime: %v", info.Mode())
	}
	os.WriteFile(filepath.Join(dir, "xray"), []byte("corrupt"), 0755)
	corrupt := sha256.Sum256([]byte("corrupt"))
	m, _ = json.Marshal(map[string]string{"archive_sha256": archiveHash, "binary_sha256": hex.EncodeToString(corrupt[:])})
	os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0644)
	if Install(context.Background(), path, "42", root) == nil {
		t.Fatal("accepted modified binary and modified local manifest instead of trusted digest")
	}
}
