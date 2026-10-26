package control

import (
	"proxyplatform/internal/core"
	"strings"
	"testing"
)

func TestCustomArtifactTrustBoundary(t *testing.T) {
	t.Setenv("XRAY_ALLOWED_REPOSITORIES", "XTLS/Xray-core,example/xray-fork")
	valid := core.Artifact{Version: "26.10.17", Variant: "compat", Arch: "amd64", Repository: "example/xray-fork", Tag: "v26.10.17-custom", URL: "https://github.com/example/xray-fork/releases/download/v26.10.17-custom/Xray-linux-64.zip", SHA256: strings.Repeat("a", 64), License: "MPL-2.0"}
	if err := validateCustomArtifact(valid); err != nil {
		t.Fatal(err)
	}
	if valid.ValidSource() {
		t.Fatal("fork accepted without authenticated approval")
	}
	valid.CustomApproved = true
	if !valid.ValidSource() {
		t.Fatal("approved fork rejected")
	}
	for name, mutate := range map[string]func(*core.Artifact){
		"repository": func(a *core.Artifact) { a.Repository = "attacker/core" },
		"query":      func(a *core.Artifact) { a.URL += "?token=secret" },
		"scheme":     func(a *core.Artifact) { a.URL = strings.Replace(a.URL, "https:", "http:", 1) },
		"digest":     func(a *core.Artifact) { a.SHA256 = "garbage" },
		"variant":    func(a *core.Artifact) { a.Variant = "../escape" },
		"tag":        func(a *core.Artifact) { a.Tag = "../main" },
		"license":    func(a *core.Artifact) { a.License = "" },
	} {
		t.Run(name, func(t *testing.T) {
			a := valid
			mutate(&a)
			if validateCustomArtifact(a) == nil {
				t.Fatal("invalid approval accepted")
			}
		})
	}
}
