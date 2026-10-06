package config_test

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
)

// parseSeeds are the fleetd.yaml bodies the other tests load, accepted and
// refused alike.
var parseSeeds = []string{
	"",
	"mode: local_trusted\n",
	"listen: {api: \"127.0.0.1:9090\"}\ntiming: {verify_grace: 3m}\n",
	"listen: {api: \"[::1]:7080\"}\n",
	"listen: {api: \"127.0.0.1:0\"}\n",
	"listen: {api: \"0.0.0.0:7080\"}\n",
	"listen: {api: \"localhost:7080\"}\n",
	"listen: {api: \"[::ffff:127.0.0.1]:7080\"}\n",
	"listen: {api: \"[::1%lo0]:7080\"}\n",
	"listen: {api: \":7080\"}\n",
	"listen: {api: \"\"}\n",
	"listen: {api: \"127.0.0.1:70000\"}\n",
	"listen: {api: \"127.0.0.1:http\"}\n",
	"listen: {agent: \"0.0.0.0\"}\n",
	"listen: {agent: \"[fe80::1%en0]:7443\"}\n",
	"listen: {metrics: \"127.0.0.1:9100\"}\n",
	"listen: {metrics: \"0.0.0.0:9100\"}\n",
	"listen: {pprof: \"127.0.0.1:6060\"}\n",
	"bogus: 1\n",
	"listen: {apii: \"127.0.0.1:1\"}\n",
	"timing: {max_sync_lagg: 1m}\n",
	"github: {app_idd: 1}\n",
	"telemetry: {otlpp: {}}\n",
	"mode: local_trusted\n---\nmode: authenticated\n",
	"mode: authenticated\n",
	"mode: open\n",
	"timing: {max_sync_lag: 10 minutes}\n",
	"timing: {ready_timeout: 0s}\n",
	"timing: {verify_grace: -1m}\n",
	"timing: {goal_file_timeout: 30}\n",
	"timing: {max_sync_lag: 1h30m, goal_file_timeout: 1ns}\n",
	"state_dir: state\n",
	"state_dir: \"\"\n",
	"state_dir: /var/lib/fleetd/\n",
	"state_dir: ~/fleet/../state\n",
	"tls: {agent_names: [\"*.example.com\"]}\n",
	"tls: {agent_names: [\"\"]}\n",
	"tls: {agent_names: [\"a b\"]}\n",
	"tls: {agent_names: [\"fe80::1%en0\"]}\n",
	"tls: {agent_names: [\"Hub.Example\", \"10.0.0.7\", \"::1\"]}\n",
	"tls: {agent_names: null}\n",
	"tls: {api_cert_file: /x.pem}\n",
	"tls: {api_tailscale: true}\n",
	"public_hosts: [\"hub.example\"]\n",
	"public_origins: [\"https://hub.example\"]\n",
	"telemetry: {otlp: {endpoint: \"http://127.0.0.1:4317\"}}\n",
	"telemetry: {otlp: {protocol: carrier-pigeon}}\n",
	"telemetry: {otlp: {protocol: grpc, headers_env: OTEL_HEADERS}, signals: [traces, logs]}\n",
	"telemetry: {signals: [profiles]}\n",
	"rate_limits: {read: 0}\n",
	"rate_limits: {read: 1, write: 2, unauthenticated: 3, metrics: 4}\n",
	"github: {app_id: -1}\n",
	"github: {app_id: 12, installation_id: 34, private_key_file: /does/not/exist.pem}\n",
	"github: {}\n",
	"discovery: {mdns: false}\n",
	"[1, 2]\n",
	"mode: [a]\n",
	"a: &x [*x]\n",
}

// FuzzParse checks what Parse promises for any input: it never panics; a
// refusal is a *config.Error naming a key; an accepted config keeps the rules
// the specs state (specRules), has an absolute, clean state_dir, and its YAML
// form is accepted again and renders to the same bytes (Duration and the
// lower-cased agent names survive a write and a read).
func FuzzParse(f *testing.F) {
	for _, s := range parseSeeds {
		f.Add([]byte(s))
	}
	_, homeErr := os.UserHomeDir()
	f.Fuzz(func(t *testing.T, body []byte) {
		c, err := config.Parse(body)
		if err != nil {
			if c != nil {
				t.Fatalf("Parse returned a config and an error %v", err)
			}
			var ce *config.Error
			if homeErr == nil && (!errors.As(err, &ce) || ce.Key == "") {
				t.Fatalf("refusal %v is not a *config.Error naming a key", err)
			}
			return
		}
		if c == nil {
			t.Fatal("Parse accepted the input but returned no config")
		}
		specRules(t, c)
		if !filepath.IsAbs(c.StateDir) || filepath.Clean(c.StateDir) != c.StateDir {
			t.Fatalf("accepted state_dir %q is not absolute and clean", c.StateDir)
		}
		first, err := yaml.Marshal(c)
		if err != nil {
			t.Fatalf("marshal an accepted config: %v", err)
		}
		again, err := config.Parse(first)
		if err != nil {
			t.Fatalf("the YAML form of an accepted config is refused: %v\n%s", err, first)
		}
		second, err := yaml.Marshal(again)
		if err != nil {
			t.Fatalf("marshal the re-parsed config: %v", err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("a write and a read changed the config:\n%s\nbecame\n%s", first, second)
		}
	})
}

// specRules checks an accepted config against the rules the specs state,
// written from 02f, 02j, 02k and 05b rather than from validate.go, so the
// check does not grade Parse by its own code. It asserts only what a spec
// states: the specs set no rule for the listen.metrics address (02j's loopback
// rule is listen.api only), for timing.* or rate_limits.* values, or for the
// spelling of tls.agent_names, so stricter checks in validate.go go unchecked.
func specRules(t *testing.T, c *config.Config) {
	t.Helper()
	if c.Mode != "local_trusted" { // 02k: authenticated is not built yet
		t.Errorf("mode %q accepted; v1 builds only local_trusted", c.Mode)
	}
	if host, _, err := net.SplitHostPort(c.Listen.API); err != nil || (host != "127.0.0.1" && host != "::1") {
		t.Errorf("listen.api %q accepted; 02f allows only 127.0.0.1 or ::1 in local_trusted", c.Listen.API)
	}
	notBuilt := map[string]bool{ // 02k "Refused at start with not built yet"
		"listen.pprof":            c.Listen.Pprof != "",
		"public_hosts":            len(c.PublicHosts) > 0,
		"public_origins":          len(c.PublicOrigins) > 0,
		"tls.api_cert_file":       c.TLS.APICertFile != "",
		"tls.api_key_file":        c.TLS.APIKeyFile != "",
		"tls.api_tailscale":       c.TLS.APITailscale,
		"telemetry.otlp.endpoint": c.Telemetry.OTLP.Endpoint != "",
	}
	for key, set := range notBuilt {
		if set {
			t.Errorf("%s accepted; 02k refuses it as not built yet", key)
		}
	}
	switch p := c.Telemetry.OTLP.Protocol; p { // 05b section 5
	case "", "grpc", "http/protobuf":
	default:
		t.Errorf("telemetry.otlp.protocol %q accepted; 05b allows grpc or http/protobuf", p)
	}
	for _, s := range c.Telemetry.Signals {
		if s != "traces" && s != "metrics" && s != "logs" {
			t.Errorf("telemetry.signals entry %q accepted; 05b allows traces, metrics or logs", s)
		}
	}
}
