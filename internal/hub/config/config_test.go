package config_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
)

// The defaults, written out by hand from the YAML block of 02j "Configuration
// file" (02j:70-86) and the timing and rate-limit rows of defaults.md.
func TestEveryDefaultMatches02j(t *testing.T) {
	t.Parallel()
	d := config.Default()
	cases := []struct {
		key       string
		got, want any
	}{
		{"mode", d.Mode, "local_trusted"},
		{"state_dir", d.StateDir, "~/.local/state/fleetd"},
		{"listen.agent", d.Listen.Agent, "0.0.0.0:7443"},
		{"listen.api", d.Listen.API, "127.0.0.1:7080"},
		{"listen.metrics", d.Listen.Metrics, ""},
		{"listen.pprof", d.Listen.Pprof, ""},
		{"tls.agent_names", len(d.TLS.AgentNames), 0},
		{"tls.api_cert_file", d.TLS.APICertFile, ""},
		{"tls.api_key_file", d.TLS.APIKeyFile, ""},
		{"tls.api_tailscale", d.TLS.APITailscale, false},
		{"public_hosts", len(d.PublicHosts), 0},
		{"public_origins", len(d.PublicOrigins), 0},
		{"discovery.mdns", d.Discovery.MDNS, true},
		{"github (absent)", d.GitHub == nil, true},
		{"timing.max_sync_lag", time.Duration(d.Timing.MaxSyncLag), 10 * time.Minute},
		{"timing.verify_grace", time.Duration(d.Timing.VerifyGrace), 10 * time.Minute},
		{"timing.ready_timeout", time.Duration(d.Timing.ReadyTimeout), 90 * time.Second},
		{"timing.goal_file_timeout", time.Duration(d.Timing.GoalFileTimeout), 30 * time.Second},
		{"rate_limits.read", d.RateLimits.Read, 600},
		{"rate_limits.write", d.RateLimits.Write, 120},
		{"rate_limits.unauthenticated", d.RateLimits.Unauthenticated, 30},
		{"rate_limits.metrics", d.RateLimits.Metrics, 60},
		{"telemetry.otlp.endpoint", d.Telemetry.OTLP.Endpoint, ""},
		{"telemetry.signals", len(d.Telemetry.Signals), 0},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Errorf("default %s = %v, want %v", tc.key, tc.got, tc.want)
			}
		})
	}
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fleetd.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func home(t *testing.T) string {
	t.Helper()
	h, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAnEmptyFileLoadsTheDefaultsWithStateDirUnderHome(t *testing.T) {
	t.Parallel()
	c, err := config.Load(writeFile(t, ""))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(home(t), ".local", "state", "fleetd"); c.StateDir != want {
		t.Errorf("state_dir = %q, want %q", c.StateDir, want)
	}
	if c.Listen.API != "127.0.0.1:7080" || c.Mode != "local_trusted" || !c.Discovery.MDNS {
		t.Errorf("defaults lost: %+v", c)
	}
}

func TestKeysNotWrittenKeepTheirDefaults(t *testing.T) {
	t.Parallel()
	c, err := config.Load(writeFile(t, "listen: {api: \"127.0.0.1:9090\"}\ntiming: {verify_grace: 3m}\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Listen.API != "127.0.0.1:9090" {
		t.Errorf("listen.api = %q, want 127.0.0.1:9090", c.Listen.API)
	}
	if c.Listen.Agent != "0.0.0.0:7443" {
		t.Errorf("listen.agent = %q, want the default 0.0.0.0:7443", c.Listen.Agent)
	}
	if got := time.Duration(c.Timing.VerifyGrace); got != 3*time.Minute {
		t.Errorf("timing.verify_grace = %v, want 3m", got)
	}
	if got := time.Duration(c.Timing.MaxSyncLag); got != 10*time.Minute {
		t.Errorf("timing.max_sync_lag = %v, want the default 10m", got)
	}
}

func TestUnknownKeysAreRefused(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, body, field string }{
		{"top level", "bogus: 1\n", "bogus"},
		{"listen", "listen: {apii: \"127.0.0.1:1\"}\n", "apii"},
		{"timing", "timing: {max_sync_lagg: 1m}\n", "max_sync_lagg"},
		{"github", "github: {app_idd: 1}\n", "app_idd"},
		{"telemetry", "telemetry: {otlpp: {}}\n", "otlpp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := config.Load(writeFile(t, tc.body))
			if err == nil {
				t.Fatal("Load accepted an unknown key")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %q", err, tc.field)
			}
		})
	}
}

func TestASecondYAMLDocumentIsRefused(t *testing.T) {
	t.Parallel()
	if _, err := config.Load(writeFile(t, "mode: local_trusted\n---\nmode: authenticated\n")); err == nil {
		t.Fatal("Load accepted two documents")
	}
}

func TestAMissingFileIsNotExist(t *testing.T) {
	t.Parallel()
	_, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load of a missing file = %v, want fs.ErrNotExist", err)
	}
}

func TestWriteDefaultCreatesA0600FileThatLoadsAsTheDefaults(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "fleetd.yaml")
	if err := config.WriteDefault(path); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dir.Mode().Perm(); got != 0o700 {
		t.Errorf("folder mode = %o, want 700", got)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want, err := config.Load(writeFile(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("written defaults load as %+v, want %+v", got, want)
	}
	if err := config.WriteDefault(path); !errors.Is(err, fs.ErrExist) {
		t.Errorf("second WriteDefault = %v, want fs.ErrExist", err)
	}
}

func TestPathPrefersTheFlagThenTheEnvironmentThenHome(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, flag, env, want string }{
		{"flag wins", "/a/flag.yaml", "/b/env.yaml", "/a/flag.yaml"},
		{"env next", "", "/b/env.yaml", "/b/env.yaml"},
		{"home last", "", "", filepath.Join(home(t), ".config", "fleetd", "fleetd.yaml")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.Path(tc.flag, func(k string) string {
				if k == "FLEETD_CONFIG" {
					return tc.env
				}
				return ""
			})
			if err != nil {
				t.Fatalf("Path: %v", err)
			}
			if got != tc.want {
				t.Errorf("Path = %q, want %q", got, tc.want)
			}
		})
	}
}
