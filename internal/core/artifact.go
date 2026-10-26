package core

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type Artifact struct {
	Variant        string   `json:"variant,omitempty"`
	Repository     string   `json:"repository,omitempty"`
	Tag            string   `json:"tag,omitempty"`
	License        string   `json:"license,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
	CustomApproved bool     `json:"custom_approved,omitempty"`
	BinarySHA256   string   `json:"binary_sha256,omitempty"`
	Version        string   `json:"version"`
	Arch           string   `json:"arch"`
	URL            string   `json:"url"`
	SHA256         string   `json:"sha256"`
}

func (a Artifact) ID() string {
	if a.Variant != "" {
		return a.Version + "-" + a.Variant
	}
	return a.Version
}

// Approval comes from the authenticated control plane or the immutable image
// manifest. Subscription fields never create an Artifact record.
func (a Artifact) ValidSource() bool {
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if strings.HasPrefix(u.Path, "/XTLS/Xray-core/releases/download/") && a.Repository == "" {
		return true
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(a.Repository) {
		return false
	}
	return (a.Repository == "XTLS/Xray-core" || a.CustomApproved) && strings.HasPrefix(u.Path, "/"+a.Repository+"/releases/download/")
}

func Install(ctx context.Context, lock, version, root string) error {
	return InstallForArchitecture(ctx, lock, version, root, runtime.GOARCH, true)
}

func InstallForArchitecture(ctx context.Context, lock, version, root, arch string, runVersion bool) error {
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported artifact architecture")
	}
	b, e := os.ReadFile(lock)
	if e != nil {
		return e
	}
	var l struct {
		Artifacts []Artifact `json:"artifacts"`
	}
	if e = json.Unmarshal(b, &l); e != nil {
		return e
	}
	var a Artifact
	for _, v := range l.Artifacts {
		if v.ID() == version && v.Arch == arch {
			a = v
			break
		}
	}
	if !a.ValidSource() || len(a.SHA256) != 64 {
		return fmt.Errorf("artifact not in approved lock for this architecture")
	}
	if _, e = hex.DecodeString(a.SHA256); e != nil {
		return e
	}
	if strings.ContainsAny(version, "/\\") || version == "" {
		return fmt.Errorf("invalid version")
	}
	if e = os.MkdirAll(root, 0755); e != nil {
		return e
	}
	lf, e := os.OpenFile(filepath.Join(root, ".install.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lf.Close()
	for {
		if e = syscall.Flock(int(lf.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e == nil {
			break
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			return e
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	// No other installer can own a temporary download while this lock is held.
	// Remove leftovers from a process killed before its deferred cleanup ran.
	if leftovers, err := os.ReadDir(root); err == nil {
		for _, entry := range leftovers {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), ".download-") {
				if err = os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
					return err
				}
			}
		}
	}
	dest := filepath.Join(root, "v"+version)
	if data, err := os.ReadFile(filepath.Join(dest, "manifest.json")); err == nil {
		var m map[string]string
		if json.Unmarshal(data, &m) == nil && m["archive_sha256"] == a.SHA256 && len(a.BinarySHA256) == 64 {
			actual, err := FileHash(filepath.Join(dest, "xray"))
			if err == nil && actual == m["binary_sha256"] && actual == a.BinarySHA256 {
				return os.Chmod(dest, 0755)
			}
		}
		return fmt.Errorf("cached artifact failed verification")
	}
	if _, e = os.Stat(dest); e == nil {
		return fmt.Errorf("unverified destination already exists")
	}
	entries, e := os.ReadDir(root)
	if e != nil {
		return e
	}
	count := 0
	var size int64
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "v") {
			count++
			if f, err := os.Stat(filepath.Join(root, entry.Name(), "xray")); err == nil {
				size += f.Size()
			}
		}
	}
	if count >= 8 || size > 896<<20 {
		return fmt.Errorf("artifact_cache_budget_exceeded")
	}
	var disk syscall.Statfs_t
	if syscall.Statfs(root, &disk) == nil && disk.Bavail*uint64(disk.Bsize) < 256<<20 {
		return fmt.Errorf("artifact_disk_reserve_required")
	}
	tmp, e := os.MkdirTemp(root, ".download-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	req, e := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
	if e != nil {
		return e
	}
	client := &http.Client{Timeout: 120 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	resp, e := artifactResponse(ctx, client, req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return &artifactHTTPError{status: resp.StatusCode}
	}
	archive := filepath.Join(tmp, "archive.zip")
	f, e := os.OpenFile(archive, os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, (96<<20)+1))
	if e == nil {
		e = f.Sync()
	}
	f.Close()
	if e != nil {
		return e
	}
	if n > 96<<20 {
		return fmt.Errorf("archive size limit exceeded")
	}
	if hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return fmt.Errorf("archive SHA-256 mismatch")
	}
	if _, e = InspectArchive(archive, a.SHA256, arch); e != nil {
		return e
	}
	z, e := zip.OpenReader(archive)
	if e != nil {
		return e
	}
	defer z.Close()
	var binary *zip.File
	for _, f := range z.File {
		if f.Name == "xray" {
			if binary != nil {
				return fmt.Errorf("duplicate binary")
			}
			binary = f
		}
	}
	if binary == nil || binary.UncompressedSize64 > 128<<20 || binary.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("invalid binary in archive")
	}
	r, e := binary.Open()
	if e != nil {
		return e
	}
	defer r.Close()
	out := filepath.Join(tmp, "xray")
	w, e := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if e != nil {
		return e
	}
	n, e = io.Copy(w, io.LimitReader(r, (128<<20)+1))
	if e == nil {
		e = w.Sync()
	}
	w.Close()
	if e != nil || n > 128<<20 {
		return fmt.Errorf("extract failed or too large: %v", e)
	}
	digest, e := FileHash(out)
	if e != nil {
		return e
	}
	if a.BinarySHA256 != "" && digest != a.BinarySHA256 {
		return fmt.Errorf("binary SHA-256 mismatch")
	}
	if runVersion {
		if _, e = Version(ctx, out, a.Version); e != nil {
			return e
		}
	}
	for _, entry := range z.File {
		if entry.Name == "LICENSE" || entry.Name == "LICENSE.txt" {
			reader, err := entry.Open()
			if err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(reader, 65537))
			reader.Close()
			if err != nil || len(data) > 65536 {
				return fmt.Errorf("invalid artifact license")
			}
			if err = os.WriteFile(filepath.Join(tmp, "LICENSE"), data, 0644); err != nil {
				return err
			}
			break
		}
	}
	m, _ := json.MarshalIndent(map[string]string{"version": version, "archive_sha256": a.SHA256, "binary_sha256": digest, "source": a.URL}, "", "  ")
	if e = os.WriteFile(filepath.Join(tmp, "manifest.json"), m, 0644); e != nil {
		return e
	}
	if e = os.Remove(archive); e != nil {
		return e
	}
	// MkdirTemp is private while downloading; the completed artifact is read by non-root workers.
	if e = os.Chmod(tmp, 0755); e != nil {
		return e
	}
	if e = os.Rename(tmp, dest); e != nil {
		return e
	}
	return nil
}
func FileHash(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func Version(ctx context.Context, path, expected string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b, e := exec.CommandContext(ctx, path, "version").Output()
	if e != nil {
		return "", fmt.Errorf("core version check failed: %w", e)
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "Xray" || fields[1] != expected {
		return "", fmt.Errorf("unexpected core version: expected %s", expected)
	}
	return line, nil
}
