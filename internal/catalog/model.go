package catalog

import (
	"encoding/json"
	"fmt"
	"proxyplatform/internal/config"
	"regexp"
	"strings"
	"time"
)

type Source struct {
	URL             string            `json:"url"`
	Headers         map[string]string `json:"headers"`
	Enabled         bool              `json:"enabled"`
	IntervalSeconds int               `json:"interval_seconds"`
	MaxStaleSeconds int               `json:"max_stale_seconds"`
	AllowPrivate    bool              `json:"allow_private"`
	CoreVersion     string            `json:"core_version"`
	Filter          Filter            `json:"filter"`
	ETag            string            `json:"etag,omitempty"`
	LastModified    string            `json:"last_modified,omitempty"`
	LastSuccess     time.Time         `json:"last_success,omitempty"`
	NextRefresh     time.Time         `json:"next_refresh,omitempty"`
	LastError       string            `json:"last_error,omitempty"`
	ProfileCount    int               `json:"profile_count"`
	PendingSnapshot string            `json:"pending_snapshot,omitempty"`
	Quota           map[string]int64  `json:"quota,omitempty"`
}
type Profile struct {
	Present         bool            `json:"present"`
	SourceID        string          `json:"source_id"`
	Name            string          `json:"name"`
	Protocol        string          `json:"protocol"`
	Host            string          `json:"host"`
	Port            int             `json:"port"`
	Transport       string          `json:"transport"`
	Security        string          `json:"security"`
	Country         string          `json:"country"`
	CountryInferred bool            `json:"country_inferred"`
	UDP             bool            `json:"udp"`
	Supported       bool            `json:"supported"`
	Reason          string          `json:"reason,omitempty"`
	Enabled         bool            `json:"enabled"`
	CoreVersion     string          `json:"core_version,omitempty"`
	CoreOverride    bool            `json:"core_override"`
	Fingerprint     string          `json:"fingerprint"`
	Identity        string          `json:"identity"`
	Outbound        json.RawMessage `json:"outbound,omitempty"`
	Warnings        []string        `json:"warnings,omitempty"`
}
type Filter struct {
	Mode        string      `json:"mode"`
	Conditions  []Condition `json:"conditions"`
	ExcludedIDs []string    `json:"excluded_ids"`
	Countries   []string    `json:"countries"`
	Protocols   []string    `json:"protocols"`
	UDPOnly     bool        `json:"udp_only"`
}
type Condition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

func (f Filter) Validate() error {
	if f.Mode != "" && f.Mode != "all" && f.Mode != "any" {
		return fmt.Errorf("filter mode must be all or any")
	}
	if len(f.Conditions) > 32 || len(f.ExcludedIDs) > 10000 {
		return fmt.Errorf("filter size limit")
	}
	for _, c := range f.Conditions {
		switch c.Field {
		case "name", "protocol", "transport", "country", "host", "source":
		default:
			return fmt.Errorf("unknown filter field")
		}
		if len(c.Value) > 1024 {
			return fmt.Errorf("filter value too long")
		}
		switch c.Operator {
		case "contains", "not_contains", "equals", "not_equals", "starts_with":
		case "regex":
			if _, e := regexp.Compile(c.Value); e != nil {
				return fmt.Errorf("RE2 expression unsupported: %s", e)
			}
		default:
			return fmt.Errorf("unknown filter operator")
		}
	}
	return nil
}
func (f Filter) Match(id string, p Profile) (bool, string) {
	return f.Compile().Match(id, p)
}

type Matcher struct {
	filter   Filter
	excluded map[string]bool
	regex    map[int]*regexp.Regexp
	invalid  bool
}

func (f Filter) Compile() Matcher {
	m := Matcher{filter: f, excluded: map[string]bool{}, regex: map[int]*regexp.Regexp{}}
	for _, id := range f.ExcludedIDs {
		m.excluded[id] = true
	}
	for i, c := range f.Conditions {
		if c.Operator == "regex" {
			re, e := regexp.Compile(c.Value)
			if e != nil {
				m.invalid = true
			} else {
				m.regex[i] = re
			}
		}
	}
	return m
}
func (m Matcher) Match(id string, p Profile) (bool, string) {
	f := m.filter
	if m.invalid {
		return false, "invalid_filter"
	}
	if m.excluded[id] {
		return false, "manual_exclusion"
	}
	if f.UDPOnly && !p.UDP {
		return false, "udp_not_supported"
	}
	if len(f.Countries) > 0 && !contains(f.Countries, p.Country) {
		return false, "country_filter"
	}
	if len(f.Protocols) > 0 && !contains(f.Protocols, p.Protocol) {
		return false, "protocol_filter"
	}
	values := map[string]string{"name": p.Name, "protocol": p.Protocol, "transport": p.Transport, "country": p.Country, "host": p.Host, "source": p.SourceID}
	matched := 0
	for i, c := range f.Conditions {
		a, b := strings.ToLower(values[c.Field]), strings.ToLower(c.Value)
		ok := false
		switch c.Operator {
		case "contains":
			ok = strings.Contains(a, b)
		case "not_contains":
			ok = !strings.Contains(a, b)
		case "equals":
			ok = a == b
		case "not_equals":
			ok = a != b
		case "starts_with":
			ok = strings.HasPrefix(a, b)
		case "regex":
			re := m.regex[i]
			ok = re != nil && re.MatchString(values[c.Field])
		}
		if ok {
			matched++
		}
	}
	if len(f.Conditions) > 0 {
		if f.Mode == "any" && matched == 0 {
			return false, "conditions"
		}
		if f.Mode != "any" && matched != len(f.Conditions) {
			return false, "conditions"
		}
	}
	return true, "included"
}
func contains(a []string, v string) bool {
	for _, s := range a {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

type Aggregate struct {
	SourceIDs   []string `json:"source_ids"`
	PrefixNames bool     `json:"prefix_names"`
	Dedupe      bool     `json:"dedupe"`
}
type Pool struct {
	AggregateID string   `json:"aggregate_id"`
	SourceIDs   []string `json:"source_ids"`
	ProfileIDs  []string `json:"profile_ids"`
	Filter      Filter   `json:"filter"`
	Enabled     bool     `json:"enabled"`
}
type Policy struct {
	Mode                 string   `json:"mode"`
	PoolID               string   `json:"pool_id"`
	ActivePaths          int      `json:"active_paths"`
	WarmReserve          int      `json:"warm_reserve"`
	StickySeconds        int      `json:"sticky_seconds"`
	RotationSeconds      int      `json:"rotation_seconds"`
	ProbeIntervalSeconds int      `json:"probe_interval_seconds"`
	ProbeURL             string   `json:"probe_url"`
	ProbeScope           string   `json:"probe_scope,omitempty"`
	ProbeFallbackURLs    []string `json:"probe_fallback_urls"`
	SpeedURL             string   `json:"speed_url"`
	ProbeBudgetBytes     int64    `json:"probe_budget_bytes"`
	MaxConnections       int      `json:"max_connections"`
	ConnectTimeoutMS     int      `json:"connect_timeout_ms"`
	FailClosed           bool     `json:"fail_closed"`
}

func (p *Policy) Defaults() {
	if p.Mode == "" {
		p.Mode = "auto"
	}
	if p.ActivePaths == 0 {
		p.ActivePaths = 4
	}
	if p.WarmReserve == 0 {
		p.WarmReserve = 2
	}
	if p.StickySeconds == 0 {
		p.StickySeconds = 600
	}
	if p.RotationSeconds == 0 {
		p.RotationSeconds = 30
	}
	if p.ProbeIntervalSeconds == 0 {
		p.ProbeIntervalSeconds = 30
	}
	if p.ProbeBudgetBytes == 0 {
		p.ProbeBudgetBytes = 128 << 20
	}
	if p.ConnectTimeoutMS == 0 {
		p.ConnectTimeoutMS = 3000
	}
	if p.MaxConnections == 0 {
		p.MaxConnections = 1024
	}
	if p.ProbeURL == "" {
		p.ProbeURL = "https://www.gstatic.com/generate_204"
		p.ProbeFallbackURLs = []string{"https://cp.cloudflare.com/generate_204"}
	}
	p.FailClosed = true
}
func (p Policy) Validate() error {
	if p.ProbeScope != "" && p.ProbeScope != "auto" && p.ProbeScope != "shared" && p.ProbeScope != "target" {
		return fmt.Errorf("invalid probe scope")
	}
	switch p.Mode {
	case "auto", "throughput", "latency", "sticky", "fallback", "round-robin", "timed", "manual":
	default:
		return fmt.Errorf("unknown policy mode")
	}
	if p.ActivePaths < 1 || p.ActivePaths > 16 || p.WarmReserve < 0 || p.WarmReserve > 8 || p.MaxConnections < 1 || p.MaxConnections > 5000 || len(p.ProbeFallbackURLs) > 2 || p.StickySeconds < 0 || p.StickySeconds > 86400 || p.RotationSeconds < 5 || p.ProbeIntervalSeconds < 10 || p.ProbeIntervalSeconds > 3600 || p.ProbeBudgetBytes < 0 || p.ProbeBudgetBytes > 10<<30 || p.ConnectTimeoutMS < 100 || p.ConnectTimeoutMS > 30000 {
		return fmt.Errorf("policy values out of range")
	}
	return nil
}

type Listener struct {
	Protocol   string   `json:"protocol"`
	Listen     string   `json:"listen"`
	PolicyID   string   `json:"policy_id"`
	Username   string   `json:"username,omitempty"`
	Password   string   `json:"password,omitempty"`
	UDP        bool     `json:"udp"`
	Enabled    bool     `json:"enabled"`
	AllowCIDRs []string `json:"allow_cidrs"`
}
type Settings struct {
	EgressPaths         []config.EgressPath `json:"egress_paths,omitempty"`
	DefaultCore         string              `json:"default_core"`
	UDPAdvertise        string              `json:"udp_advertise"`
	UDPPortMin          int                 `json:"udp_port_min"`
	UDPPortMax          int                 `json:"udp_port_max"`
	OfflineLeaseSeconds int                 `json:"offline_lease_seconds"`
	DownloadMbps        int                 `json:"download_mbps"`
	UploadMbps          int                 `json:"upload_mbps"`
	ProxyBind           string              `json:"proxy_bind"`
}

func Decode[T any](data json.RawMessage) (T, error) {
	var v T
	e := json.Unmarshal(data, &v)
	return v, e
}
