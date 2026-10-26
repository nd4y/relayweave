package control

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"proxyplatform/internal/core"
	"time"
)

func (s *Server) inspectArtifact(ctx context.Context, a core.Artifact) (string, error) {
	dir, e := os.MkdirTemp(s.DataDir, ".artifact-inspect-")
	if e != nil {
		return "", errCode("artifact_disk_unavailable")
	}
	defer os.RemoveAll(dir)
	request, e := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
	if e != nil {
		return "", errCode("artifact_download_failed")
	}
	client := &http.Client{Timeout: 90 * time.Second}
	response, e := client.Do(request)
	if e != nil {
		return "", errCode("artifact_download_failed")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", errCode("artifact_download_failed")
	}
	path := filepath.Join(dir, "archive.zip")
	file, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", errCode("artifact_disk_unavailable")
	}
	n, e := io.Copy(file, io.LimitReader(response.Body, (96<<20)+1))
	closed := file.Close()
	if e != nil || closed != nil || n > 96<<20 {
		return "", errCode("artifact_download_size_or_integrity")
	}
	digest, e := core.InspectArchive(path, a.SHA256, a.Arch)
	if e != nil {
		return "", errCode("artifact_archive_integrity_failed")
	}
	return digest, nil
}
