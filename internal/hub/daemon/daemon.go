package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/hub/daemon"

// Options configure [Init] and [Run]. Zero values are the production
// behavior; the function fields are seams for tests.
type Options struct {
	// ConfigPath is fleetd.yaml (config.Path resolves --config,
	// $FLEETD_CONFIG and the default).
	ConfigPath string
	// Version is service.version on every signal.
	Version string
	// Hostname feeds the default SAN list; nil means os.Hostname.
	Hostname func() (string, error)
	// Now is the clock for certificate issue and renewal; nil means time.Now.
	Now func() time.Time
	// RenewTicks drives the certificate check in Run; nil means an hourly
	// ticker that Run owns.
	RenewTicks <-chan time.Time
	// Telemetry is used as given (and not shut down) when set; otherwise
	// Init and Run build their own, logging to LogOutput (nil: stderr).
	Telemetry *telemetry.Telemetry
	LogOutput io.Writer
	// Ready, when set, is called once every listener is serving.
	Ready func(Addrs)
}

// Addrs are the bound listener addresses; Metrics is nil without
// listen.metrics.
type Addrs struct {
	Agent, API, Metrics net.Addr
}

// Reason is the closed label set of start failures: the reason label of
// fleet_hub_start_failures_total and of the hub.start_failed log line.
type Reason string

// Start failure reasons.
const (
	ReasonConfig          Reason = "config_invalid"   // fleetd.yaml missing (run), unreadable or refused
	ReasonLockHeld        Reason = "lock_held"        // another fleetd holds <state_dir>/fleetd.lock (#92)
	ReasonLooseMode       Reason = "mode_loose"       // state folder or state.db open to other users
	ReasonStoreMissing    Reason = "store_missing"    // run: no state folder or no state.db; run fleetd init
	ReasonStoreExists     Reason = "store_exists"     // init: state.db already exists
	ReasonIdentityMissing Reason = "identity_missing" // run: state.db has no hub_identity row
	ReasonStore           Reason = "store_failed"     // state.db could not be created or opened
	ReasonIdentity        Reason = "identity_failed"  // init: minting the CA, certificate or salt failed
	ReasonCert            Reason = "cert_failed"      // run: the server certificate could not be loaded or re-issued
	ReasonListen          Reason = "listen_failed"    // a listener could not bind
)

// StartError is why init or run did not start. The message may name paths;
// it is for the person at the terminal and never goes into telemetry.
type StartError struct {
	Command string
	Reason  Reason
	Err     error
}

func (e *StartError) Error() string { return e.Err.Error() }
func (e *StartError) Unwrap() error { return e.Err }

// session is one command's telemetry, owned or borrowed.
type session struct {
	tel      *telemetry.Telemetry
	owned    bool
	command  string
	failures metric.Int64Counter
}

func newSession(command string, opts Options) (*session, error) {
	s := &session{tel: opts.Telemetry, command: command}
	if s.tel == nil {
		instance, err := newUUID()
		if err != nil {
			return nil, err
		}
		s.tel, err = telemetry.New(telemetry.Config{
			Service: telemetry.Hub, Version: opts.Version, InstanceID: instance, LogOutput: opts.LogOutput,
		})
		if err != nil {
			return nil, err
		}
		s.owned = true
	}
	var err error
	s.failures, err = s.tel.Meter(scope).Int64Counter(s.tel.MetricPrefix()+"_start_failures_total",
		metric.WithDescription("fleetd init and run attempts that refused to start, by command and reason."))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("daemon: counter: %w", err), s.close())
	}
	return s, nil
}

// fail logs and counts a start failure and returns it as a *StartError.
// Only the reason and a closed err_kind are recorded, never the error text.
func (s *session) fail(reason Reason, err error) error {
	ctx := context.Background()
	s.failures.Add(ctx, 1, metric.WithAttributes(
		attribute.String("command", s.command), attribute.String("reason", string(reason))))
	s.tel.Logger.Error("hub.start_failed", "command", s.command, "reason", string(reason), "err_kind", errKind(err))
	return &StartError{Command: s.command, Reason: reason, Err: err}
}

func (s *session) close() error {
	if !s.owned {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.tel.Shutdown(ctx)
}

// errKind is the closed err_kind class of 05b.
func errKind(err error) string {
	var ce *config.Error
	switch {
	case errors.As(err, &ce):
		return "parse"
	case errors.Is(err, lock.ErrHeld), errors.Is(err, statedb.ErrLooseMode), errors.Is(err, statedb.ErrExists),
		errors.Is(err, store.ErrMissing), errors.Is(err, statedb.ErrNoIdentity), errors.Is(err, os.ErrNotExist):
		return "refused"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "io"
	}
}

// newUUID is a random (version 4) UUID.
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("daemon: random id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// names is tls.agent_names, or the default list from the hostname.
func (o Options) names(cfg *config.Config, defaults func(string) []string) ([]string, error) {
	if len(cfg.TLS.AgentNames) > 0 {
		return cfg.TLS.AgentNames, nil
	}
	hostname := o.Hostname
	if hostname == nil {
		hostname = os.Hostname
	}
	h, err := hostname()
	if err != nil {
		return nil, fmt.Errorf("daemon: hostname: %w", err)
	}
	return defaults(h), nil
}
