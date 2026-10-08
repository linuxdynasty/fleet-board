package config_test

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
)

// refusal loads body and returns the *config.Error it must fail with.
func refusal(t *testing.T, body string) *config.Error {
	t.Helper()
	_, err := config.Load(writeFile(t, body))
	if err == nil {
		t.Fatalf("Load accepted:\n%s", body)
	}
	var ce *config.Error
	if !errors.As(err, &ce) {
		t.Fatalf("Load error %v is not a *config.Error", err)
	}
	return ce
}

// 02f Modes: local_trusted listens on 127.0.0.1 or ::1 only, no override.
func TestLocalTrustedAPIAcceptsOnlyTheTwoLoopbackAddresses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:7080", true},
		{"[::1]:7080", true},
		{"127.0.0.1:0", true},
		{"0.0.0.0:7080", false},
		{"[::]:7080", false},
		{"localhost:7080", false},
		{"192.168.1.20:7080", false},
		{"10.0.0.5:7080", false},
		{"127.0.0.2:7080", false},
		{"[::ffff:127.0.0.1]:7080", false},
		{"[::1%lo0]:7080", false},
		{":7080", false},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()
			body := "listen: {api: \"" + tc.addr + "\"}\n"
			if tc.ok {
				if _, err := config.Load(writeFile(t, body)); err != nil {
					t.Fatalf("Load refused %s: %v", tc.addr, err)
				}
				return
			}
			if ce := refusal(t, body); ce.Key != "listen.api" {
				t.Errorf("refusal names %q, want listen.api", ce.Key)
			}
		})
	}
}

func TestRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body, key string
		notBuilt        bool
	}{
		{"mode authenticated", "mode: authenticated\n", "mode", true},
		{"mode unknown", "mode: open\n", "mode", false},
		{"pprof", "listen: {pprof: \"127.0.0.1:6060\"}\n", "listen.pprof", true},
		{"metrics not loopback", "listen: {metrics: \"0.0.0.0:9100\"}\n", "listen.metrics", false},
		{"metrics localhost", "listen: {metrics: \"localhost:9100\"}\n", "listen.metrics", false},
		{"api empty", "listen: {api: \"\"}\n", "listen.api", false},
		{"api port too big", "listen: {api: \"127.0.0.1:70000\"}\n", "listen.api", false},
		{"api port not a number", "listen: {api: \"127.0.0.1:http\"}\n", "listen.api", false},
		{"agent no port", "listen: {agent: \"0.0.0.0\"}\n", "listen.agent", false},
		{"bad duration", "timing: {max_sync_lag: 10 minutes}\n", "timing.max_sync_lag", false},
		{"zero duration", "timing: {ready_timeout: 0s}\n", "timing.ready_timeout", false},
		{"negative duration", "timing: {verify_grace: -1m}\n", "timing.verify_grace", false},
		{"duration as a number", "timing: {goal_file_timeout: 30}\n", "timing.goal_file_timeout", false},
		{"relative state_dir", "state_dir: state\n", "state_dir", false},
		{"empty state_dir", "state_dir: \"\"\n", "state_dir", false},
		{"agent name wildcard", "tls: {agent_names: [\"*.example.com\"]}\n", "tls.agent_names", false},
		{"agent name empty", "tls: {agent_names: [\"\"]}\n", "tls.agent_names", false},
		{"agent name with a space", "tls: {agent_names: [\"a b\"]}\n", "tls.agent_names", false},
		{"agent name zoned IPv6", "tls: {agent_names: [\"fe80::1%en0\"]}\n", "tls.agent_names", false},
		{"agent listen zoned IPv6", "listen: {agent: \"[fe80::1%en0]:7443\"}\n", "listen.agent", false},
		{"public_hosts", "public_hosts: [\"hub.example\"]\n", "public_hosts", true},
		{"public_origins", "public_origins: [\"https://hub.example\"]\n", "public_origins", true},
		{"api cert file", "tls: {api_cert_file: /x.pem}\n", "tls.api_cert_file", true},
		{"api tailscale", "tls: {api_tailscale: true}\n", "tls.api_tailscale", true},
		{"otlp endpoint", "telemetry: {otlp: {endpoint: \"http://127.0.0.1:4317\"}}\n", "telemetry.otlp.endpoint", true},
		{"otlp protocol", "telemetry: {otlp: {protocol: carrier-pigeon}}\n", "telemetry.otlp.protocol", false},
		{"telemetry signal", "telemetry: {signals: [profiles]}\n", "telemetry.signals", false},
		{"rate limit zero", "rate_limits: {read: 0}\n", "rate_limits.read", false},
		{"github negative app id", "github: {app_id: -1}\n", "github.app_id", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ce := refusal(t, tc.body)
			if ce.Key != tc.key {
				t.Errorf("refusal names %q, want %q (%v)", ce.Key, tc.key, ce)
			}
			if ce.NotBuilt != tc.notBuilt {
				t.Errorf("NotBuilt = %v, want %v (%v)", ce.NotBuilt, tc.notBuilt, ce)
			}
			if tc.notBuilt && !strings.Contains(ce.Error(), "not built yet") {
				t.Errorf("message %q does not say not built yet", ce)
			}
		})
	}
}

// A key written with no value (empty, ~ or null) is written, so it is refused
// under its own name rather than keeping its default (#109). In YAML, ~ is no
// value, not the home folder.
func TestAKeyWithNoValueIsRefused(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, body, key string }{
		{"duration empty", "timing:\n  verify_grace:\n", "timing.verify_grace"},
		{"duration tilde", "timing: {verify_grace: ~}\n", "timing.verify_grace"},
		{"duration null", "timing: {verify_grace: null}\n", "timing.verify_grace"},
		{"string", "mode: ~\n", "mode"},
		{"listen address", "listen: {api: null}\n", "listen.api"},
		{"number", "rate_limits: {read: ~}\n", "rate_limits.read"},
		{"bool", "discovery: {mdns: ~}\n", "discovery.mdns"},
		{"bool tls", "tls: {api_tailscale: null}\n", "tls.api_tailscale"},
		{"whole section", "timing:\n", "timing"},
		{"whole section tilde", "listen: ~\n", "listen"},
		{"state_dir tilde", "state_dir: ~\n", "state_dir"},
		{"list", "public_hosts: ~\n", "public_hosts"},
		{"nested", "telemetry: {otlp: {protocol: ~}}\n", "telemetry.otlp.protocol"},
		{"optional section", "github: ~\n", "github"},
		{"flow empty", "listen: {api: }\n", "listen.api"},
		{"capitalized Null", "mode: Null\n", "mode"},
		{"merged", "listen: {<<: {api: ~}}\n", "listen.<<.api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ce := refusal(t, tc.body)
			if ce.Key != tc.key {
				t.Errorf("refusal names %q, want %q (%v)", ce.Key, tc.key, ce)
			}
			if !strings.Contains(ce.Reason, "no value") {
				t.Errorf("reason %q does not say the key has no value", ce.Reason)
			}
		})
	}
}

// A list item or a key name with no value is refused too: yaml.v3 skips a
// mapping entry whose key is null, which KnownFields never reports.
func TestAnItemOrKeyNameWithNoValueIsRefused(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, body, key, reason string }{
		{"list item", "tls: {agent_names: [hub.example, ~]}\n", "tls.agent_names", "an item in the list has no value"},
		{"top-level null key", "~: 1\n", "(file)", "a key with no name"},
		{"nested null key", "listen: {~: x}\n", "listen", "a key with no name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ce := refusal(t, tc.body)
			if ce.Key != tc.key {
				t.Errorf("refusal names %q, want %q (%v)", ce.Key, tc.key, ce)
			}
			if !strings.Contains(ce.Reason, tc.reason) {
				t.Errorf("reason %q, want it to say %q", ce.Reason, tc.reason)
			}
		})
	}
}

// A file of only comments, or an empty one, still means "all defaults".
func TestAnEmptyFileKeepsTheDefaults(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"", "# nothing set\n", "---\n"} {
		if _, err := config.Load(writeFile(t, body)); err != nil {
			t.Errorf("Load(%q): %v", body, err)
		}
	}
}

// The github section is checked for shape only: the key file is never
// opened, so a path that does not exist loads.
func TestGitHubKeysAreCheckedForShapeOnly(t *testing.T) {
	t.Parallel()
	body := "github: {app_id: 12, installation_id: 34, private_key_file: /does/not/exist.pem}\n"
	c, err := config.Load(writeFile(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.GitHub == nil || c.GitHub.AppID != 12 || c.GitHub.PrivateKeyFile != "/does/not/exist.pem" {
		t.Errorf("github = %+v", c.GitHub)
	}
}

func TestAgentNamesAreLowerCasedAndKept(t *testing.T) {
	t.Parallel()
	c, err := config.Load(writeFile(t, "tls: {agent_names: [\"Hub.Example\", \"10.0.0.7\", \"::1\"]}\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"hub.example", "10.0.0.7", "::1"}
	if strings.Join(c.TLS.AgentNames, ",") != strings.Join(want, ",") {
		t.Errorf("agent_names = %q, want %q", c.TLS.AgentNames, want)
	}
}

// sun_path is 104 bytes on darwin and 108 on Linux, including the closing
// NUL, so <state_dir>/admin.sock may be at most 103 or 107 bytes long.
func TestAdminSocketPathLengthIsChecked(t *testing.T) {
	t.Parallel()
	var limit int
	switch runtime.GOOS {
	case "darwin":
		limit = 104
	case "linux":
		limit = 108
	default:
		t.Skipf("no sun_path size written down for %s", runtime.GOOS)
	}
	const sock = "/admin.sock"
	dirOfLen := func(n int) string { return "/" + strings.Repeat("d", n-1) }
	fits := dirOfLen(limit - 1 - len(sock))
	tooLong := dirOfLen(limit - len(sock))
	if got := len(fits + sock); got != limit-1 {
		t.Fatalf("test setup: fitting path is %d bytes", got)
	}

	c, err := config.Load(writeFile(t, "state_dir: "+fits+"\n"))
	if err != nil {
		t.Fatalf("a %d-byte socket path was refused: %v", limit-1, err)
	}
	if got := config.AdminSocketPath(c.StateDir); got != fits+sock {
		t.Errorf("AdminSocketPath = %q, want %q", got, fits+sock)
	}
	if ce := refusal(t, "state_dir: "+tooLong+"\n"); ce.Key != "state_dir" {
		t.Errorf("a %d-byte socket path refusal names %q, want state_dir", limit, ce.Key)
	}
}
