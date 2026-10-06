package config

import (
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Error is one refused key. NotBuilt marks a key this build knows but does not
// implement yet: fleetd refuses it rather than accept and ignore it.
type Error struct {
	Key      string
	Reason   string
	NotBuilt bool
}

func (e *Error) Error() string {
	if e.NotBuilt {
		return fmt.Sprintf("fleetd.yaml: %s: %s: not built yet", e.Key, e.Reason)
	}
	return fmt.Sprintf("fleetd.yaml: %s: %s", e.Key, e.Reason)
}

func refuse(key, format string, args ...any) *Error {
	return &Error{Key: key, Reason: fmt.Sprintf(format, args...)}
}

func notBuilt(key, reason string) *Error {
	return &Error{Key: key, Reason: reason, NotBuilt: true}
}

// AdminSocketPath is where the admin socket lives (02j "State directory").
func AdminSocketPath(stateDir string) string { return filepath.Join(stateDir, "admin.sock") }

func (c *Config) validate() error {
	checks := []func() *Error{
		c.checkMode, c.checkStateDir, c.checkListen, c.checkTLS,
		c.checkPublic, c.checkTiming, c.checkRateLimits, c.checkGitHub, c.checkTelemetry,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) checkMode() *Error {
	switch c.Mode {
	case "local_trusted":
		return nil
	case "authenticated":
		return notBuilt("mode", "authenticated mode (sign-in, HTTPS API) arrives in v3")
	default:
		return refuse("mode", "%q is not local_trusted or authenticated", c.Mode)
	}
}

func (c *Config) checkStateDir() *Error {
	if c.StateDir == "" || !filepath.IsAbs(c.StateDir) {
		return refuse("state_dir", "must be an absolute path or start with ~/")
	}
	c.StateDir = filepath.Clean(c.StateDir)
	if n := len(AdminSocketPath(c.StateDir)); n >= maxSocketPath {
		return refuse("state_dir", "the admin socket path would be %d bytes; this OS allows %d, so pick a shorter folder",
			n, maxSocketPath-1)
	}
	return nil
}

// port returns the host and checks the port of a host:port address.
func port(key, addr string) (string, *Error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", refuse(key, "%q is not host:port", addr)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 0 || n > 65535 {
		return "", refuse(key, "port %q is not a number from 0 to 65535", p)
	}
	if strings.Contains(host, "%") {
		return "", refuse(key, "%q has an IPv6 zone; a certificate cannot name one, so use an address without %%zone", addr)
	}
	return host, nil
}

// loopback is the exact-address rule of 02f: 127.0.0.1 or ::1, never a name
// or another loopback address.
func loopback(key, addr string) *Error {
	host, err := port(key, addr)
	if err != nil {
		return err
	}
	if host != "127.0.0.1" && host != "::1" {
		return refuse(key, "%q must listen on 127.0.0.1 or [::1] (loopback only; there is no override)", addr)
	}
	return nil
}

func (c *Config) checkListen() *Error {
	if c.Listen.Agent == "" {
		return refuse("listen.agent", "must be set")
	}
	if _, err := port("listen.agent", c.Listen.Agent); err != nil {
		return err
	}
	if c.Listen.API == "" {
		return refuse("listen.api", "must be set")
	}
	if err := loopback("listen.api", c.Listen.API); err != nil {
		return err
	}
	if c.Listen.Metrics != "" {
		if err := loopback("listen.metrics", c.Listen.Metrics); err != nil {
			return err
		}
	}
	if c.Listen.Pprof != "" {
		return notBuilt("listen.pprof", "the pprof listener")
	}
	return nil
}

func (c *Config) checkTLS() *Error {
	for i, name := range c.TLS.AgentNames {
		name = strings.ToLower(name)
		if strings.Contains(name, "%") {
			return refuse("tls.agent_names", "%q has an IPv6 zone; a certificate cannot name one", name)
		}
		if !validName(name) {
			return refuse("tls.agent_names", "%q is not an IP address or a DNS name", name)
		}
		c.TLS.AgentNames[i] = name
	}
	switch {
	case c.TLS.APICertFile != "":
		return notBuilt("tls.api_cert_file", "the HTTPS API belongs to authenticated mode")
	case c.TLS.APIKeyFile != "":
		return notBuilt("tls.api_key_file", "the HTTPS API belongs to authenticated mode")
	case c.TLS.APITailscale:
		return notBuilt("tls.api_tailscale", "the HTTPS API belongs to authenticated mode")
	}
	return nil
}

// validName is an IP address, or a DNS name of letters, digits and hyphens
// with no wildcard.
func validName(name string) bool {
	if _, err := netip.ParseAddr(name); err == nil {
		return true
	}
	if name == "" || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func (c *Config) checkPublic() *Error {
	if len(c.PublicHosts) > 0 {
		return notBuilt("public_hosts", "the Host allow-list belongs to authenticated mode; local_trusted accepts the loopback names only")
	}
	if len(c.PublicOrigins) > 0 {
		return notBuilt("public_origins", "the Origin allow-list belongs to authenticated mode; local_trusted accepts the loopback origins only")
	}
	return nil
}

func (c *Config) checkTiming() *Error {
	for _, d := range []struct {
		key string
		v   Duration
	}{
		{"timing.max_sync_lag", c.Timing.MaxSyncLag},
		{"timing.verify_grace", c.Timing.VerifyGrace},
		{"timing.ready_timeout", c.Timing.ReadyTimeout},
		{"timing.goal_file_timeout", c.Timing.GoalFileTimeout},
	} {
		if time.Duration(d.v) <= 0 {
			return refuse(d.key, "must be a positive duration such as 10m or 90s")
		}
	}
	return nil
}

func (c *Config) checkRateLimits() *Error {
	for _, r := range []struct {
		key string
		v   int
	}{
		{"rate_limits.read", c.RateLimits.Read},
		{"rate_limits.write", c.RateLimits.Write},
		{"rate_limits.unauthenticated", c.RateLimits.Unauthenticated},
		{"rate_limits.metrics", c.RateLimits.Metrics},
	} {
		if r.v <= 0 {
			return refuse(r.key, "must be a positive number per minute")
		}
	}
	return nil
}

func (c *Config) checkGitHub() *Error {
	if c.GitHub == nil {
		return nil
	}
	if c.GitHub.AppID < 0 {
		return refuse("github.app_id", "must not be negative")
	}
	if c.GitHub.InstallationID < 0 {
		return refuse("github.installation_id", "must not be negative")
	}
	return nil
}

func (c *Config) checkTelemetry() *Error {
	if c.Telemetry.OTLP.Endpoint != "" {
		return notBuilt("telemetry.otlp.endpoint", "OTLP export")
	}
	switch c.Telemetry.OTLP.Protocol {
	case "", "grpc", "http/protobuf":
	default:
		return refuse("telemetry.otlp.protocol", "%q is not grpc or http/protobuf", c.Telemetry.OTLP.Protocol)
	}
	for _, s := range c.Telemetry.Signals {
		switch s {
		case "traces", "metrics", "logs":
		default:
			return refuse("telemetry.signals", "%q is not traces, metrics or logs", s)
		}
	}
	return nil
}
