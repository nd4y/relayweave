package control

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"

	"proxyplatform/internal/core"
)

var releaseComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,95}$`)

func validateCustomArtifact(a core.Artifact) error {
	if !coreVersion.MatchString(a.Version) || a.Variant == "" || !coreSelector.MatchString(a.ID()) || (a.Arch != "amd64" && a.Arch != "arm64") {
		return errCode("artifact_identity_invalid")
	}
	allowed := false
	repositories := os.Getenv("XRAY_ALLOWED_REPOSITORIES")
	if repositories == "" {
		repositories = "XTLS/Xray-core"
	}
	for _, repo := range strings.Split(repositories, ",") {
		if strings.TrimSpace(repo) == a.Repository {
			allowed = true
		}
	}
	if !allowed {
		return errCode("artifact_repository_not_allowed")
	}
	u, err := url.Parse(a.URL)
	if err != nil || !releaseComponent.MatchString(a.Tag) || !releaseComponent.MatchString(path.Base(u.Path)) || !strings.HasSuffix(u.Path, ".zip") || a.URL != "https://github.com/"+a.Repository+"/releases/download/"+a.Tag+"/"+path.Base(u.Path) {
		return errCode("artifact_release_url_invalid")
	}
	if b, err := hex.DecodeString(a.SHA256); err != nil || len(b) != 32 {
		return errCode("artifact_digest_invalid")
	}
	if a.BinarySHA256 != "" {
		if b, err := hex.DecodeString(a.BinarySHA256); err != nil || len(b) != 32 {
			return errCode("artifact_digest_invalid")
		}
	}
	if len(a.License) == 0 || len(a.License) > 100 || len(a.Capabilities) > 32 {
		return errCode("artifact_metadata_invalid")
	}
	for _, capability := range a.Capabilities {
		if !releaseComponent.MatchString(capability) {
			return errCode("artifact_metadata_invalid")
		}
	}
	return nil
}

func (s *Server) approveArtifact(ctx context.Context, op Operation) (any, error) {
	var a core.Artifact
	if json.Unmarshal(op.Payload, &a) != nil {
		return nil, errCode("artifact_invalid")
	}
	if err := validateCustomArtifact(a); err != nil {
		return nil, err
	}
	digest, err := s.inspectArtifact(ctx, a)
	if err != nil {
		return nil, err
	}
	if a.BinarySHA256 != "" && a.BinarySHA256 != digest {
		return nil, errCode("artifact_binary_digest_mismatch")
	}
	a.BinarySHA256 = digest
	a.CustomApproved = true
	return s.queueArtifactInstall(ctx, a)
}
