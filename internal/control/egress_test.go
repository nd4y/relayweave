package control

import (
	"encoding/json"
	"proxyplatform/internal/catalog"
	"proxyplatform/internal/config"
	"proxyplatform/internal/store"
	"testing"
)

func TestSettingsAcceptEgressPathsAndRejectInvalid(t *testing.T) {
	settings := catalog.Settings{DefaultCore: "26.10.17", UDPAdvertise: "auto", UDPPortMin: 31000, UDPPortMax: 31255, OfflineLeaseSeconds: 86400, DownloadMbps: 800, UploadMbps: 500}
	data, _ := json.Marshal(settings)
	old := &store.Object{Data: data}
	s := &Server{}
	patch := json.RawMessage(`{"egress_paths":[{"id":"isp2","name":"Secondary","source_ip":"192.0.2.12","enabled":true,"weight":5,"download_mbps":475,"upload_mbps":475}]}`)
	result, e := s.validate("settings", patch, old)
	if e != nil {
		t.Fatal("valid source bindings rejected", e)
	}
	var decoded catalog.Settings
	json.Unmarshal(result, &decoded)
	if len(decoded.EgressPaths) != 1 || decoded.EgressPaths[0].ID != "isp2" {
		t.Fatal("ISP setting lost")
	}
	decoded.EgressPaths[0].Enabled = false
	patch, _ = json.Marshal(map[string][]config.EgressPath{"egress_paths": decoded.EgressPaths})
	if _, e = s.validate("settings", patch, old); e == nil {
		t.Fatal("all paths disabled accepted")
	}
}
