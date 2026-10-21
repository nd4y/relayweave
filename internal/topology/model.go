// Package topology contains the privacy allowlist shared by worker and control plane.
// No target, client address, URL, credential or raw configuration belongs here.
package topology

import "time"

type Candidate struct {
	Route               string   `json:"route"`
	Engine              string   `json:"engine"`
	Active              int64    `json:"active"`
	Score               *float64 `json:"score,omitempty"`
	Reason              string   `json:"reason"`
	Group               string   `json:"group,omitempty"`
	SpeedBPS            int64    `json:"speed_bps"`
	MeasurementAt       int64    `json:"measurement_at"`
	Factors             []string `json:"factors,omitempty"`
	EffectiveRTTMS      float64  `json:"effective_rtt_ms,omitempty"`
	ConsecutiveFailures int64    `json:"consecutive_failures,omitempty"`
	ErrorEWMA           int64    `json:"error_ewma,omitempty"`
	SpeedUsedBPS        int64    `json:"speed_used_bps,omitempty"`
	SpeedSource         string   `json:"speed_source,omitempty"`
	PassiveAt           int64    `json:"passive_at,omitempty"`
	CeilingBPS          int64    `json:"ceiling_bps,omitempty"`
	Formula             string   `json:"formula,omitempty"`
}
type Decision struct {
	Epoch      string      `json:"epoch,omitempty"`
	Worker     string      `json:"worker"`
	ID         string      `json:"id"`
	FlowID     string      `json:"flow_id,omitempty"`
	At         time.Time   `json:"at"`
	FinishedAt time.Time   `json:"finished_at"`
	Generation int64       `json:"generation"`
	Policy     string      `json:"policy"`
	Mode       string      `json:"mode"`
	Method     string      `json:"method"`
	Listener   string      `json:"listener"`
	Protocol   string      `json:"protocol"`
	Selected   string      `json:"selected,omitempty"`
	Attempted  string      `json:"attempted,omitempty"`
	Engine     string      `json:"engine,omitempty"`
	Reason     string      `json:"reason"`
	Candidates []Candidate `json:"candidates"`
	Truncated  bool        `json:"truncated"`
	PreparedAt time.Time   `json:"prepared_at"`
}
type Route struct {
	ID            string    `json:"id"`
	Engine        string    `json:"engine"`
	Core          string    `json:"core"`
	Version       string    `json:"version"`
	BuildSHA256   string    `json:"build_sha256,omitempty"`
	Subscription  string    `json:"subscription"`
	ServerHost    string    `json:"server_host"`
	ServerIPs     []string  `json:"server_ips"`
	DNSAt         time.Time `json:"dns_at"`
	Group         string    `json:"group"`
	State         string    `json:"state"`
	Active        int64     `json:"active"`
	Connections   int64     `json:"connections"`
	Failures      int64     `json:"failures"`
	Up            int64     `json:"up"`
	Down          int64     `json:"down"`
	SpeedBPS      int64     `json:"speed_bps"`
	MeasurementAt int64     `json:"measurement_at"`
	RTTUS         int64     `json:"rtt_us"`
	ValidUntil    time.Time `json:"valid_until"`
}
type Listener struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"`
	Policy   string `json:"policy"`
}
type Frame struct {
	Changes     []Change   `json:"changes,omitempty"`
	Worker      string     `json:"worker"`
	Epoch       string     `json:"epoch"`
	Sequence    uint64     `json:"sequence"`
	At          time.Time  `json:"at"`
	Generation  int64      `json:"generation"`
	CatalogSize int        `json:"catalog_size"`
	Routes      []Route    `json:"routes"`
	Listeners   []Listener `json:"listeners"`
	Decisions   []Decision `json:"decisions"`
	Dropped     uint64     `json:"dropped"`
	Sampled     uint64     `json:"sampled"`
	SampleEvery int        `json:"sample_every"`
	UDPActive   int64      `json:"udp_active"`
	LeaseUntil  int64      `json:"lease_until"`
	Truncated   bool       `json:"truncated"`
}

// Change is a difference between observed worker frames. It does not infer
// the selector's cause, an exact transition time, or existing tunnel migration.
type Change struct {
	ID         string    `json:"id"`
	Worker     string    `json:"worker"`
	Epoch      string    `json:"epoch,omitempty"`
	Kind       string    `json:"kind"`
	Route      string    `json:"route,omitempty"`
	Engine     string    `json:"engine,omitempty"`
	Before     string    `json:"before"`
	After      string    `json:"after"`
	At         time.Time `json:"at"`
	PreviousAt time.Time `json:"previous_at"`
	Generation int64     `json:"generation"`
	Source     string    `json:"source"`
}
type Snapshot struct {
	Schema        int            `json:"schema"`
	ServerTime    time.Time      `json:"server_time"`
	Cursor        string         `json:"cursor"`
	Workers       []Frame        `json:"workers"`
	Completeness  string         `json:"completeness"`
	WindowSeconds int            `json:"window_seconds"`
	Limitations   []string       `json:"limitations"`
	Retention     map[string]int `json:"retention"`
}
