package policy_test

import (
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

const secret = "SECRETSECRET-do-not-echo"

func TestParseAcceptsEveryKeyOfTheTable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	doc := `
# a comment
schema_version: "1"
default_server: observe
max_active: 3
max_total: 8
max_windows: 20
allowed_harnesses: [claude, codex]
allowed_repo_roots: ["` + root + `"]
local_approval:
  - command_kind: KillWindow
    required: true
  - command_kind: Drain
rate_limits:
  - command_kind: SendKeys
    per_minute: 30
cert_lifetime: 2160h
attach_idle_timeout: 30m
spool_cap_bytes: 1073741824
block_on_battery: true
window_name_template: "{harness} {work}"
idle_threshold: 90s
share_identity_email: false
min_probe_interval: 10m
labels: [gpu, build-box]
metrics_listen: 127.0.0.1:9100
pprof_listen: "[::1]:6060"
telemetry:
  otlp:
    endpoint: http://localhost:4317
    protocol: grpc
    headers_env: OTEL_HEADERS
    insecure: true
  signals: [traces, metrics]
exec:
  raw: true
`
	res := policy.Parse([]byte(doc))
	if res.Problem != nil {
		t.Fatalf("Parse = %v, want none", res.Problem)
	}
	p := res.Policy
	if res.SchemaVersion != "1" || p.SchemaVersion != "1" {
		t.Errorf("schema version = %q / %q", res.SchemaVersion, p.SchemaVersion)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"default_server", p.DefaultServer != nil && *p.DefaultServer == policy.ServerObserve},
		{"max_active", p.MaxActive != nil && *p.MaxActive == 3},
		{"max_total", p.MaxTotal != nil && *p.MaxTotal == 8},
		{"max_windows", p.MaxWindows != nil && *p.MaxWindows == 20},
		{"allowed_harnesses", len(p.AllowedHarnesses) == 2 && p.AllowedHarnesses[1] == "codex"},
		{"allowed_repo_roots", len(p.AllowedRepoRoots) == 1 && p.AllowedRepoRoots[0] == root},
		{"local_approval", len(p.LocalApproval) == 2 && p.LocalApproval[0].Required && !p.LocalApproval[1].Required},
		{"rate_limits", len(p.RateLimits) == 1 && p.RateLimits[0].PerMinute == 30},
		{"cert_lifetime", p.CertLifetime != nil && *p.CertLifetime == 2160*time.Hour},
		{"attach_idle_timeout", p.AttachIdleTimeout != nil && *p.AttachIdleTimeout == 30*time.Minute},
		{"spool_cap_bytes", p.SpoolCapBytes != nil && *p.SpoolCapBytes == 1<<30},
		{"block_on_battery", p.BlockOnBattery != nil && *p.BlockOnBattery},
		{"window_name_template", p.WindowNameTemplate != nil && *p.WindowNameTemplate == "{harness} {work}"},
		{"idle_threshold", p.IdleThreshold != nil && *p.IdleThreshold == 90*time.Second},
		{"share_identity_email", p.ShareIdentityEmail != nil && !*p.ShareIdentityEmail},
		{"min_probe_interval", p.MinProbeInterval != nil && *p.MinProbeInterval == 10*time.Minute},
		{"labels", len(p.Labels) == 2 && p.Labels[1] == "build-box"},
		{"metrics_listen", p.MetricsListen != nil && *p.MetricsListen == "127.0.0.1:9100"},
		{"pprof_listen", p.PprofListen != nil && *p.PprofListen == "[::1]:6060"},
		{"telemetry", p.Telemetry != nil && p.Telemetry.OTLP.Endpoint == "http://localhost:4317" && len(p.Telemetry.Signals) == 2},
		{"exec.raw ignored", p.ExecRawIgnored},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s was not read as written: %+v", c.name, p)
		}
	}
}

func TestAnAbsentKeyIsNoOpinionAndAnEmptyListIsAnOpinion(t *testing.T) {
	t.Parallel()
	absent := policy.Parse([]byte("schema_version: \"1\"\n")).Policy
	if absent.AllowedHarnesses != nil || absent.AllowedRepoRoots != nil || absent.MaxActive != nil || absent.DefaultServer != nil || absent.Labels != nil {
		t.Errorf("absent keys = %+v, want every one unset", absent)
	}
	empty := policy.Parse([]byte("schema_version: \"1\"\nallowed_harnesses: []\nallowed_repo_roots: []\nlocal_approval: []\n")).Policy
	if empty.AllowedHarnesses == nil || len(empty.AllowedHarnesses) != 0 {
		t.Errorf("allowed_harnesses: [] = %#v, want an empty, non-nil list (no harness)", empty.AllowedHarnesses)
	}
	if empty.AllowedRepoRoots == nil || empty.LocalApproval == nil {
		t.Errorf("empty lists = %+v, want non-nil", empty)
	}
	null := policy.Parse([]byte("schema_version: \"1\"\nallowed_harnesses:\nmax_active: ~\n")).Policy
	if null.AllowedHarnesses != nil || null.MaxActive != nil {
		t.Errorf("null values = %+v, want no opinion", null)
	}
}

func TestAnEmptyFileOrOnlyCommentsIsTheDefaultSchema(t *testing.T) {
	t.Parallel()
	for _, doc := range []string{"", "\n", "# nothing here\n", "---\n"} {
		res := policy.Parse([]byte(doc))
		if res.Problem != nil || res.SchemaVersion != "1" || res.Policy.SchemaVersion != "1" {
			t.Errorf("Parse(%q) = %+v, want valid, schema_version 1", doc, res)
		}
	}
}

func TestParseRefusesWithAClosedOneLineReason(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cases := []struct {
		name, doc, code, msg string
	}{
		{"unknown key", "schema_version: \"1\"\nmax_activ: 2\n", "unknown_key", "line 2: unknown key"},
		{"unknown key in a rule", "local_approval:\n  - command_kind: Drain\n    sudo: true\n", "unknown_key", "line 3: local_approval: unknown key"},
		{"duplicate key", "max_active: 2\nmax_active: 3\n", "duplicate_key", "line 2: max_active: given twice"},
		{"unknown schema version", "schema_version: \"2\"\n", "schema_version", `line 1: schema_version: only "1" is known`},
		{"schema version not a string", "schema_version: 1\n", "bad_type", "line 1: schema_version: must be text"},
		{"max_active low", "max_active: 0\n", "out_of_range", "line 1: max_active: must be 1 to 16"},
		{"max_active high", "max_active: 17\n", "out_of_range", "line 1: max_active: must be 1 to 16"},
		{"max_total high", "max_total: 33\n", "out_of_range", "line 1: max_total: must be 1 to 32"},
		{"max_windows low", "max_windows: 0\n", "out_of_range", "line 1: max_windows: must be at least 1"},
		{"max_active negative", "max_active: -1\n", "bad_type", "line 1: max_active: must be a whole number"},
		{"max_active text", "max_active: " + secret + "\n", "bad_type", "line 1: max_active: must be a whole number"},
		{"default_server", "default_server: " + secret + "\n", "out_of_range", "line 1: default_server: must be none, observe or control"},
		{"top level list", "- " + secret + "\n", "bad_type", "line 1: the file must be a map of keys to values"},
		{"bad yaml", "max_active: [" + secret + "\n", "bad_yaml", "line 1: not valid YAML"},
		{"duration without a unit", "cert_lifetime: 90\n", "bad_type", "line 1: cert_lifetime: must be a duration such as 30m or 2160h"},
		{"cert_lifetime over a year", "cert_lifetime: 8761h\n", "out_of_range", "line 1: cert_lifetime: must be up to 365 days (8760h)"},
		{"cert_lifetime zero", "cert_lifetime: 0s\n", "out_of_range", "line 1: cert_lifetime: must be above zero"},
		{"min_probe_interval short", "min_probe_interval: 59s\n", "out_of_range", "line 1: min_probe_interval: must be at least 1m"},
		{"relative root", "allowed_repo_roots: [repos]\n", "bad_value", "line 1: allowed_repo_roots: every entry must be an absolute path"},
		{"missing root", "allowed_repo_roots: [" + root + "/nope]\n", "bad_value", "line 1: allowed_repo_roots: every entry must be an existing folder"},
		{"unknown kind", "local_approval:\n  - command_kind: " + secret + "\n", "bad_value", "line 2: local_approval: command_kind is not a command kind"},
		{"rate limit zero", "rate_limits:\n  - command_kind: Drain\n    per_minute: 0\n", "out_of_range", "line 3: rate_limits: per_minute must be at least 1"},
		{"rate limit twice", "rate_limits:\n  - {command_kind: Drain, per_minute: 1}\n  - {command_kind: Drain, per_minute: 2}\n", "duplicate_key", "line 3: rate_limits: Drain is given twice"},
		{"harness name", "allowed_harnesses: [\"" + secret + " x\"]\n", "bad_value", "line 1: allowed_harnesses: names are lowercase letters, digits and -"},
		{"label", "labels: [Big]\n", "bad_value", "line 1: labels: each label is [a-z0-9][a-z0-9-]{0,31}"},
		{"metrics_listen not loopback", "metrics_listen: 0.0.0.0:9100\n", "bad_value", "line 1: metrics_listen: must be a loopback host:port"},
		{"pprof_listen no port", "pprof_listen: 127.0.0.1\n", "bad_value", "line 1: pprof_listen: must be a loopback host:port"},
		{"telemetry key", "telemetry:\n  otlp:\n    token: " + secret + "\n", "unknown_key", "line 3: telemetry.otlp: unknown key"},
		{"telemetry protocol", "telemetry:\n  otlp:\n    protocol: ftp\n", "out_of_range", "line 3: telemetry.otlp.protocol: must be grpc or http/protobuf"},
		{"telemetry insecure remote", "telemetry:\n  otlp:\n    endpoint: https://collector.example.com\n    insecure: true\n", "bad_value", "line 4: telemetry.otlp.insecure: only for a loopback endpoint"},
		{"telemetry signals", "telemetry:\n  signals: [spans]\n", "out_of_range", "line 2: telemetry.signals: each is traces, metrics or logs"},
		{"headers_env is a name", "telemetry:\n  otlp:\n    headers_env: \"Bearer " + secret + "\"\n", "bad_value", "line 3: telemetry.otlp.headers_env: must be an environment variable name"},
		{"exec unknown", "exec:\n  shell: true\n", "unknown_key", "line 2: exec: unknown key"},
		{"template control", "window_name_template: \"a\\nb\"\n", "bad_value", "line 1: window_name_template: no control characters, at most 128 bytes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			res := policy.Parse([]byte(c.doc))
			if res.Problem == nil {
				t.Fatalf("Parse accepted %q", c.doc)
			}
			if res.Problem.Code != c.code || res.Problem.Error() != c.msg {
				t.Errorf("Problem = {%s} %q, want {%s} %q", res.Problem.Code, res.Problem.Error(), c.code, c.msg)
			}
			if strings.Contains(res.Problem.Error(), secret) || len(res.Problem.Error()) > 256 {
				t.Errorf("reason %q echoes file text or is over 256 bytes", res.Problem.Error())
			}
		})
	}
}

func TestParseKeepsTheSchemaVersionOfAnInvalidFile(t *testing.T) {
	t.Parallel()
	res := policy.Parse([]byte("max_active: 99\nschema_version: \"1\"\n"))
	if res.Problem == nil || res.SchemaVersion != "1" {
		t.Errorf("Parse = %+v, want a problem and schema_version 1 (it parsed before the failure)", res)
	}
	res = policy.Parse([]byte("schema_version: \"" + secret + "\"\n"))
	if res.Problem == nil || res.SchemaVersion != "" {
		t.Errorf("Parse = %+v, want a problem and no schema version echoed", res)
	}
}

func TestAFileOverTheSizeCapIsRefusedByParseToo(t *testing.T) {
	t.Parallel()
	res := policy.Parse(append([]byte("# "), make([]byte, policy.MaxFileBytes)...))
	if res.Problem == nil || res.Problem.Code != policy.CodeTooLarge || res.Problem.Error() != "the file is over 64 KiB" {
		t.Errorf("Parse = %+v, want too_large", res)
	}
}
