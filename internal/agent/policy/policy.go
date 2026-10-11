package policy

import "time"

// SchemaVersion is the only schema_version this build knows (03e).
const SchemaVersion = "1"

// ServerMode is default_server: none, observe or control, loosest last.
type ServerMode string

// The three modes of default_server (ADR 0011).
const (
	ServerNone    ServerMode = "none"
	ServerObserve ServerMode = "observe"
	ServerControl ServerMode = "control"
)

// ApprovalRule is one local_approval entry: the command kind needs
// `fleet-agent approve` when Required.
type ApprovalRule struct {
	CommandKind string
	Required    bool
}

// RateLimit is one rate_limits entry.
type RateLimit struct {
	CommandKind string
	PerMinute   uint32
}

// OTLP is telemetry.otlp (05b section 5). Nothing reads it before the row
// that adds the exporter; it is validated so a bad file is caught at load.
type OTLP struct {
	Endpoint   string
	Protocol   string
	HeadersEnv string
	Insecure   bool
}

// Telemetry is the telemetry key.
type Telemetry struct {
	OTLP    OTLP
	Signals []string
}

// Policy is a valid policy.yaml. A pointer or a nil list is a key the file
// does not state: no local opinion, so the built-in default or the hub's value
// applies. A non-nil empty list is an opinion: no harness, no root, no rule.
// The merge with the hub's settings is not here.
type Policy struct {
	SchemaVersion string

	DefaultServer *ServerMode
	MaxActive     *uint32
	MaxTotal      *uint32
	MaxWindows    *uint32

	AllowedHarnesses []string
	AllowedRepoRoots []string
	LocalApproval    []ApprovalRule
	RateLimits       []RateLimit

	CertLifetime       *time.Duration
	AttachIdleTimeout  *time.Duration
	IdleThreshold      *time.Duration
	MinProbeInterval   *time.Duration
	SpoolCapBytes      *uint64
	BlockOnBattery     *bool
	ShareIdentityEmail *bool

	WindowNameTemplate *string
	Labels             []string
	MetricsListen      *string
	PprofListen        *string
	Telemetry          *Telemetry

	// ExecRawIgnored is true when the file states exec.raw. The key is
	// known and never honoured (03e): the agent logs that it ignored it.
	ExecRawIgnored bool
}
