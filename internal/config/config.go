package config

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"time"
)

type Core struct {
	Family   string `json:"family,omitempty"`
	Artifact string `json:"artifact,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	ID       string `json:"id"`
	Version  string `json:"version"`
	Binary   string `json:"binary"`
}
type Route struct {
	EstablishedOnly  bool             `json:"established_only,omitempty"`
	ServerAllowCIDRs []string         `json:"server_allow_cidrs,omitempty"`
	Previous         []RevisionPermit `json:"previous,omitempty"`
	CheckServer      bool             `json:"check_server,omitempty"`
	AllowPrivate     bool             `json:"allow_private,omitempty"`
	ProfileID        string           `json:"profile_id,omitempty"`
	EgressID         string           `json:"egress_id,omitempty"`
	Fingerprint      string           `json:"fingerprint,omitempty"`
	UDP              bool             `json:"udp,omitempty"`
	ID               string           `json:"id"`
	Core             string           `json:"core"`
	Endpoint         string           `json:"endpoint"`
	Outbound         json.RawMessage  `json:"outbound"`
	ValidUntil       time.Time        `json:"valid_until,omitempty"`
	CapacityGroup    string           `json:"capacity_group,omitempty"`
	ServerHost       string           `json:"server_host,omitempty"`
}
type Listener struct {
	ID         string   `json:"id"`
	Protocol   string   `json:"protocol"`
	Listen     string   `json:"listen"`
	Route      string   `json:"route,omitempty"`
	Username   string   `json:"username,omitempty"`
	Password   string   `json:"password,omitempty"`
	UDP        bool     `json:"udp,omitempty"`
	Policy     string   `json:"policy,omitempty"`
	AllowCIDRs []string `json:"allow_cidrs,omitempty"`
}
type Policy struct {
	ID                   string   `json:"id"`
	Mode                 string   `json:"mode"`
	Routes               []string `json:"routes"`
	StickySeconds        int      `json:"sticky_seconds"`
	RotationSeconds      int      `json:"rotation_seconds"`
	ProbeIntervalSeconds int      `json:"probe_interval_seconds"`
	ProbeURL             string   `json:"probe_url"`
	ProbeScope           string   `json:"probe_scope,omitempty"`
	ProbeFallbackURLs    []string `json:"probe_fallback_urls,omitempty"`
	SpeedURL             string   `json:"speed_url"`
	ProbeBudgetBytes     int64    `json:"probe_budget_bytes"`
	MaxConnections       int      `json:"max_connections"`
	ConnectTimeoutMS     int      `json:"connect_timeout_ms"`
	ActivePaths          int      `json:"active_paths"`
	WarmReserve          int      `json:"warm_reserve"`
}
type UDP struct {
	Bind             string `json:"bind"`
	Advertise        string `json:"advertise"`
	PortMin          int    `json:"port_min"`
	PortMax          int    `json:"port_max"`
	IdleSeconds      int    `json:"idle_seconds"`
	MaxDatagramBytes int    `json:"max_datagram_bytes"`
}
type Config struct {
	UDPProbeTargets      []string     `json:"udp_probe_targets,omitempty"`
	EgressPaths          []EgressPath `json:"egress_paths,omitempty"`
	Generation           int64        `json:"generation,omitempty"`
	DownloadMbps         int          `json:"download_mbps,omitempty"`
	UploadMbps           int          `json:"upload_mbps,omitempty"`
	Managed              bool         `json:"managed,omitempty"`
	Policies             []Policy     `json:"policies,omitempty"`
	LeaseUntil           time.Time    `json:"lease_until,omitempty"`
	Cores                []Core       `json:"cores"`
	Routes               []Route      `json:"routes"`
	Listeners            []Listener   `json:"listeners"`
	UDP                  UDP          `json:"udp"`
	StateDir             string       `json:"state_dir"`
	MetricsListen        string       `json:"metrics_listen"`
	ProbeURL             string       `json:"probe_url"`
	ConnectTimeoutMS     int          `json:"connect_timeout_ms"`
	MaxConnections       int          `json:"max_connections"`
	ProbeIntervalSeconds int          `json:"probe_interval_seconds,omitempty"`
	ProbeFallbackURLs    []string     `json:"probe_fallback_urls,omitempty"`
}

var id = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`)

func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	return Decode(f)
}
func Decode(r io.Reader) (Config, error) {
	var c Config
	d := json.NewDecoder(io.LimitReader(r, 32<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, fmt.Errorf("trailing configuration data")
	}
	return c, c.Validate()
}
func (c *Config) Validate() error {
	if len(c.UDPProbeTargets) > 2 {
		return fmt.Errorf("UDP probe target limit")
	}
	for _, target := range c.UDPProbeTargets {
		host, port, err := net.SplitHostPort(target)
		n, pe := strconv.Atoi(port)
		if err != nil || pe != nil || net.ParseIP(host) == nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid UDP DNS probe target")
		}
	}
	if err := ValidateEgressPaths(c.EgressPaths); err != nil {
		return err
	}
	if (!c.Managed && (len(c.Cores) == 0 || len(c.Routes) == 0)) || len(c.Cores) > 8 || len(c.Routes) > 10000 {
		return fmt.Errorf("cores must be 1..8 and routes 1..10000")
	}
	cores := map[string]bool{}
	routes := map[string]bool{}
	endpoints := map[string]bool{}
	for _, v := range c.Cores {
		if !id.MatchString(v.ID) || cores[v.ID] || v.Binary == "" || v.Version == "" {
			return fmt.Errorf("invalid or duplicate core")
		}
		if v.SHA256 != "" {
			if b, e := hex.DecodeString(v.SHA256); e != nil || len(b) != 32 {
				return fmt.Errorf("invalid core digest")
			}
		}
		cores[v.ID] = true
	}
	for _, v := range c.Routes {
		if err := ValidateServerCIDRs(v.ServerAllowCIDRs); err != nil {
			return err
		}
		if len(v.Previous) > 2 {
			return fmt.Errorf("route revision overlap limit")
		}
		if err := c.ValidateRouteEgress(v); err != nil {
			return err
		}
		h, _, err := net.SplitHostPort(v.Endpoint)
		ip := net.ParseIP(h)
		if !id.MatchString(v.ID) || routes[v.ID] || !cores[v.Core] || err != nil || ip == nil || !ip.IsLoopback() || endpoints[v.Endpoint] {
			return fmt.Errorf("invalid route %q: require unique ID and private loopback endpoint", v.ID)
		}
		var outbound map[string]any
		if json.Unmarshal(v.Outbound, &outbound) != nil || outbound["protocol"] == nil {
			return fmt.Errorf("route %s has no outbound protocol", v.ID)
		}
		routes[v.ID] = true
		endpoints[v.Endpoint] = true
	}
	if (!c.Managed && len(c.Listeners) == 0) || len(c.Listeners) > 32 {
		return fmt.Errorf("listeners must be 1..32")
	}
	policies := map[string]bool{}
	if len(c.Policies) > 8 {
		return fmt.Errorf("at most 8 policies per worker")
	}
	for _, p := range c.Policies {
		if len(c.EgressPaths) > 0 && (p.Mode == "auto" || p.Mode == "throughput" || p.Mode == "round-robin") {
			enabled := 0
			for _, path := range c.EgressPaths {
				if path.Enabled {
					enabled++
				}
			}
			if p.ActivePaths < enabled {
				return fmt.Errorf("active paths must cover enabled ISPs")
			}
		}
		if p.ProbeScope != "" && p.ProbeScope != "auto" && p.ProbeScope != "shared" && p.ProbeScope != "target" {
			return fmt.Errorf("invalid probe scope")
		}
		if !id.MatchString(p.ID) || policies[p.ID] || (!c.Managed && len(p.Routes) == 0) || p.ActivePaths < 1 || p.ActivePaths > 16 || p.WarmReserve < 0 || p.WarmReserve > 8 {
			return fmt.Errorf("invalid policy")
		}
		policies[p.ID] = true
		for _, r := range p.Routes {
			if !routes[r] {
				return fmt.Errorf("policy route missing")
			}
		}
	}
	ids := map[string]bool{}
	listens := map[string]bool{}
	for _, v := range c.Listeners {
		if !id.MatchString(v.ID) || ids[v.ID] || listens[v.Listen] || (v.Protocol != "http" && v.Protocol != "socks5") || (v.Route != "" && !routes[v.Route]) || (v.Policy != "" && !policies[v.Policy]) {
			return fmt.Errorf("invalid listener %q", v.ID)
		}
		if _, _, err := net.SplitHostPort(v.Listen); err != nil {
			return err
		}
		if (v.Username == "") != (v.Password == "") || len(v.Username) > 255 || len(v.Password) > 255 {
			return fmt.Errorf("both username and password required, max 255 bytes")
		}
		if v.UDP && v.Protocol != "socks5" {
			return fmt.Errorf("UDP requires SOCKS5")
		}
		for _, cidr := range v.AllowCIDRs {
			if _, _, e := net.ParseCIDR(cidr); e != nil {
				return fmt.Errorf("invalid listener CIDR")
			}
		}
		ids[v.ID] = true
		listens[v.Listen] = true
	}
	if c.UDP.PortMin < 1024 || c.UDP.PortMax < c.UDP.PortMin || c.UDP.PortMax > 65535 || c.UDP.PortMax-c.UDP.PortMin > 1023 || net.ParseIP(c.UDP.Bind) == nil || (c.UDP.Advertise != "auto" && net.ParseIP(c.UDP.Advertise) == nil) {
		return fmt.Errorf("invalid UDP address or relay range")
	}
	if c.StateDir == "" || c.MetricsListen == "" || c.ProbeURL == "" {
		return fmt.Errorf("state_dir, metrics_listen, probe_url required")
	}
	if c.ConnectTimeoutMS == 0 {
		c.ConnectTimeoutMS = 3000
	}
	if c.ConnectTimeoutMS < 100 || c.ConnectTimeoutMS > 30000 {
		return fmt.Errorf("invalid connect timeout")
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = 1024
	}
	if c.MaxConnections < 1 || c.MaxConnections > 100000 {
		return fmt.Errorf("invalid connection limit")
	}
	if c.UDP.IdleSeconds == 0 {
		c.UDP.IdleSeconds = 60
	}
	if c.UDP.MaxDatagramBytes == 0 {
		c.UDP.MaxDatagramBytes = 7168
	}
	if c.UDP.MaxDatagramBytes < 128 || c.UDP.MaxDatagramBytes > 7168 {
		return fmt.Errorf("Xray adapter requires 128..7168 bytes per SOCKS UDP frame")
	}
	if c.UDP.IdleSeconds < 1 || c.UDP.IdleSeconds > 3600 {
		return fmt.Errorf("invalid UDP idle timeout")
	}
	return ValidateDeploymentBinds(*c)
}
func (c Config) Timeout() time.Duration { return time.Duration(c.ConnectTimeoutMS) * time.Millisecond }
