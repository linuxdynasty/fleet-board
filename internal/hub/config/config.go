package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is fleetd.yaml (02j "Configuration file"). [Load] returns it with
// state_dir made absolute and every key checked.
type Config struct {
	Mode          string     `yaml:"mode"`
	StateDir      string     `yaml:"state_dir"`
	Listen        Listen     `yaml:"listen"`
	TLS           TLS        `yaml:"tls"`
	PublicHosts   []string   `yaml:"public_hosts"`
	PublicOrigins []string   `yaml:"public_origins"`
	Discovery     Discovery  `yaml:"discovery"`
	GitHub        *GitHub    `yaml:"github,omitempty"`
	Timing        Timing     `yaml:"timing"`
	RateLimits    RateLimits `yaml:"rate_limits"`
	Telemetry     Telemetry  `yaml:"telemetry"`
}

// Listen holds the listener addresses (02j "Listeners").
type Listen struct {
	Agent   string `yaml:"agent"`
	API     string `yaml:"api"`
	Metrics string `yaml:"metrics"`
	Pprof   string `yaml:"pprof"`
}

// TLS names the agent certificate's SANs and, in authenticated mode, the API
// certificate.
type TLS struct {
	// AgentNames replaces the default SAN list when it is not empty.
	AgentNames   []string `yaml:"agent_names"`
	APICertFile  string   `yaml:"api_cert_file"`
	APIKeyFile   string   `yaml:"api_key_file"`
	APITailscale bool     `yaml:"api_tailscale"`
}

// Discovery is mDNS advertising (ADR 0023, 0028).
type Discovery struct {
	MDNS bool `yaml:"mdns"`
}

// GitHub is the section `fleetd setup github` writes. fleetd checks its shape
// only; it never opens the key files at load.
type GitHub struct {
	AppID            int64  `yaml:"app_id"`
	InstallationID   int64  `yaml:"installation_id"`
	PrivateKeyFile   string `yaml:"private_key_file"`
	ClientID         string `yaml:"client_id"`
	ClientSecretFile string `yaml:"client_secret_file"`
}

// Timing holds the constants no proto carries (defaults.md).
type Timing struct {
	MaxSyncLag      Duration `yaml:"max_sync_lag"`
	VerifyGrace     Duration `yaml:"verify_grace"`
	ReadyTimeout    Duration `yaml:"ready_timeout"`
	GoalFileTimeout Duration `yaml:"goal_file_timeout"`
}

// RateLimits are per minute (defaults.md).
type RateLimits struct {
	Read            int `yaml:"read"`
	Write           int `yaml:"write"`
	Unauthenticated int `yaml:"unauthenticated"`
	Metrics         int `yaml:"metrics"`
}

// Telemetry is the optional OTLP export of 05b section 5.
type Telemetry struct {
	OTLP    OTLP     `yaml:"otlp"`
	Signals []string `yaml:"signals"`
}

// OTLP names a collector; the header secret is named by an environment
// variable, never written here.
type OTLP struct {
	Endpoint   string `yaml:"endpoint"`
	Protocol   string `yaml:"protocol"`
	HeadersEnv string `yaml:"headers_env"`
	Insecure   bool   `yaml:"insecure"`
}

// Duration is a Go duration written as a string ("10m", "90s").
type Duration time.Duration

// UnmarshalYAML reads a string that time.ParseDuration accepts. Anything else
// (a bare number has no unit) becomes -1, which validation refuses under the
// key's own name.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	*d = -1
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return nil
	}
	if v, err := time.ParseDuration(n.Value); err == nil {
		*d = Duration(v)
	}
	return nil
}

// MarshalYAML writes the duration the way UnmarshalYAML reads it.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Default is the 02j configuration, with state_dir not yet expanded.
func Default() Config {
	return Config{
		Mode:          "local_trusted",
		StateDir:      "~/.local/state/fleetd",
		Listen:        Listen{Agent: "0.0.0.0:7443", API: "127.0.0.1:7080"},
		TLS:           TLS{AgentNames: []string{}},
		PublicHosts:   []string{},
		PublicOrigins: []string{},
		Discovery:     Discovery{MDNS: true},
		Telemetry:     Telemetry{Signals: []string{}},
		Timing: Timing{
			MaxSyncLag:      Duration(10 * time.Minute),
			VerifyGrace:     Duration(10 * time.Minute),
			ReadyTimeout:    Duration(90 * time.Second),
			GoalFileTimeout: Duration(30 * time.Second),
		},
		RateLimits: RateLimits{Read: 600, Write: 120, Unauthenticated: 30, Metrics: 60},
	}
}

// Load reads path strictly (an unknown key or a second document is refused),
// lays it over [Default], expands a leading ~ in state_dir and validates every
// key. A missing file is an error that wraps fs.ErrNotExist. Refusals are
// *[Error].
func Load(path string) (*Config, error) {
	body, err := os.ReadFile(path) //nolint:gosec // G304: path is the operator's own config path (--config, $FLEETD_CONFIG or the default); reading it is the job
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return Parse(body)
}

// Parse is [Load] on bytes already read; nil or empty is the defaults.
func Parse(body []byte) (*Config, error) {
	var err error
	c := Default()
	dec := yaml.NewDecoder(bytes.NewReader(body))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, &Error{Key: "(file)", Reason: err.Error()}
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, &Error{Key: "(file)", Reason: "holds more than one YAML document"}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return nil, &Error{Key: "(file)", Reason: err.Error()}
	}
	if err := refuseNulls(&doc, ""); err != nil {
		return nil, err
	}
	if c.StateDir, err = expandHome(c.StateDir); err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// refuseNulls refuses a key written with no value (empty, ~ or null), under
// its dotted name. yaml.v3 never calls UnmarshalYAML for a null and leaves a
// plain field as it was, so without this the default would stay silently. A
// list item or a key name with no value is refused the same way (yaml.v3
// skips an entry whose key is null).
func refuseNulls(n *yaml.Node, key string) *Error {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			if err := refuseNulls(c, key); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.ScalarNode && k.Tag == "!!null" {
				at := key
				if at == "" {
					at = "(file)"
				}
				return refuse(at, "holds a key with no name")
			}
			name := k.Value
			if key != "" {
				name = key + "." + name
			}
			if err := refuseNulls(n.Content[i+1], name); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode && c.Tag == "!!null" {
				return refuse(key, "an item in the list has no value")
			}
			if err := refuseNulls(c, key); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		if n.Tag == "!!null" && key != "" {
			return refuse(key, "has no value; write one, or remove the key to keep its default")
		}
	case yaml.AliasNode:
		// YAML defines an anchor before any alias to it, so the walk has
		// already checked what an alias points to.
	}
	return nil
}

func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: state_dir: %w", err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// Path is the configuration file to read: the --config flag, else
// $FLEETD_CONFIG, else ~/.config/fleetd/fleetd.yaml (02j).
func Path(flag string, getenv func(string) string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	if env := getenv("FLEETD_CONFIG"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	return filepath.Join(home, ".config", "fleetd", "fleetd.yaml"), nil
}

const header = "# fleetd configuration (docs/specs/02j-hub-runtime.md). fleetd reads it\n" +
	"# at start; a change needs a restart. Unknown keys are refused.\n"

// WriteDefault creates path with the defaults, mode 0600, and its folder
// (0700) when missing. It never replaces a file: an existing one is an error
// that wraps fs.ErrExist.
func WriteDefault(path string) error {
	body, err := yaml.Marshal(Default())
	if err != nil {
		return fmt.Errorf("config: render defaults: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: path is the operator's own config path; O_EXCL never replaces a file
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	_, err = f.Write(append([]byte(header), body...))
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return errors.Join(fmt.Errorf("config: write defaults: %w", err), os.Remove(path))
	}
	return nil
}
