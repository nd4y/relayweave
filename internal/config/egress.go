package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
)

// EgressPath selects a local source address. The network operator must enforce
// a strict source-based WAN route; a bind alone is not evidence of an ISP.
type EgressPath struct {
	WeightMode   string `json:"weight_mode,omitempty"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	SourceIP     string `json:"source_ip"`
	Enabled      bool   `json:"enabled"`
	Weight       int    `json:"weight"`
	DownloadMbps int    `json:"download_mbps"`
	UploadMbps   int    `json:"upload_mbps"`
}

func ValidateEgressPaths(paths []EgressPath) error {
	if len(paths) > 4 {
		return fmt.Errorf("at most four ISP paths")
	}
	ids, sources := map[string]bool{}, map[string]bool{}
	enabled := 0
	for _, p := range paths {
		if p.WeightMode != "" && p.WeightMode != "auto" && p.WeightMode != "manual" {
			return fmt.Errorf("invalid ISP weight mode")
		}
		ip := net.ParseIP(p.SourceIP)
		if !id.MatchString(p.ID) || ids[p.ID] || len(p.Name) > 80 || ip == nil || ip.IsUnspecified() || ip.IsMulticast() || sources[ip.String()] || p.Weight < 1 || p.Weight > 1000 || p.DownloadMbps < 1 || p.DownloadMbps > 100000 || p.UploadMbps < 1 || p.UploadMbps > 100000 {
			return fmt.Errorf("invalid ISP path")
		}
		ids[p.ID], sources[ip.String()] = true, true
		if p.Enabled {
			enabled++
		}
	}
	if len(paths) > 0 && enabled == 0 {
		return fmt.Errorf("at least one ISP path must be enabled")
	}
	return nil
}

func (r Route) Profile() string {
	if r.ProfileID != "" {
		return r.ProfileID
	}
	return r.ID
}
func (c Config) Egress(id string) (EgressPath, bool) {
	for _, p := range c.EgressPaths {
		if p.ID == id {
			return p, true
		}
	}
	return EgressPath{}, false
}
func (c Config) ValidateRouteEgress(r Route) error {
	if len(c.EgressPaths) == 0 {
		if r.EgressID != "" || r.ProfileID != "" {
			return fmt.Errorf("unconfigured ISP path")
		}
		return nil
	}
	p, ok := c.Egress(r.EgressID)
	var out map[string]any
	if !ok || !p.Enabled || !id.MatchString(r.ProfileID) || json.Unmarshal(r.Outbound, &out) != nil || out["sendThrough"] != net.ParseIP(p.SourceIP).String() {
		return fmt.Errorf("route ISP source mismatch")
	}
	return nil
}

// ExpandEgress is called once by the compiler, after profile filtering. All
// policy references are rewritten, while subscription guard IDs stay profiles.
func (c *Config) ExpandEgress() error {
	if err := ValidateEgressPaths(c.EgressPaths); err != nil {
		return err
	}
	if len(c.EgressPaths) == 0 {
		return nil
	}
	routes := []Route{}
	refs := map[string][]string{}
	for _, r := range c.Routes {
		if r.EgressID != "" || r.ProfileID != "" {
			return fmt.Errorf("ISP paths already expanded")
		}
		for _, p := range c.EgressPaths {
			if !p.Enabled {
				continue
			}
			var out map[string]any
			if err := json.Unmarshal(r.Outbound, &out); err != nil {
				return err
			}
			// A provider-supplied detour/interface/mark can bypass the selected source.
			// Reject rather than silently rewrite unsupported network semantics.
			if proxy, ok := out["proxySettings"].(map[string]any); ok && proxy["tag"] != "" && proxy["tag"] != nil {
				return fmt.Errorf("ISP path cannot contain proxy detour")
			}
			if stream, ok := out["streamSettings"].(map[string]any); ok {
				if sock, ok := stream["sockopt"].(map[string]any); ok {
					for _, key := range []string{"dialerProxy", "interface", "mark"} {
						if value, ok := sock[key]; ok && value != "" && value != nil && value != float64(0) {
							return fmt.Errorf("ISP path conflicts with socket binding")
						}
					}
				}
			}
			out["sendThrough"] = net.ParseIP(p.SourceIP).String()
			copy := r
			sum := sha256.Sum256([]byte(r.ID + "/" + p.ID))
			copy.ID = "path_" + hex.EncodeToString(sum[:16])
			copy.ProfileID, copy.EgressID = r.ID, p.ID
			copy.Endpoint = net.JoinHostPort("127.0.0.1", strconv.Itoa(45000+len(routes)))
			copy.Outbound, _ = json.Marshal(out)
			refs[r.ID] = append(refs[r.ID], copy.ID)
			routes = append(routes, copy)
			if len(routes) > 10000 {
				return fmt.Errorf("ISP path catalogue exceeds route limit")
			}
		}
	}
	for i, p := range c.Policies {
		ids := []string{}
		for _, id := range p.Routes {
			ids = append(ids, refs[id]...)
		}
		c.Policies[i].Routes = ids
	}
	for i, l := range c.Listeners {
		if l.Route != "" {
			if len(refs[l.Route]) != 1 {
				return fmt.Errorf("multi ISP requires a policy listener")
			}
			c.Listeners[i].Route = refs[l.Route][0]
		}
	}
	c.Routes = routes
	return nil
}
