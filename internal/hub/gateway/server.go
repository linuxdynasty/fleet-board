package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// RevokedSet answers whether a certificate serial (lowercase hex,
// ca.SerialHex) is revoked. It is called on every handshake that carries a
// client certificate, so it must not block; revocation.List is one.
type RevokedSet interface {
	Revoked(serial string) bool
}

// ServerOptions build [NewServer]. Certs, Store, Revoked and Telemetry are
// required.
type ServerOptions struct {
	Certs *Certs
	Store *store.Store
	// Identity is the hub_identity row read at start: the CA, fleet_id, salt
	// and hub_addresses an EnrollResponse carries.
	Identity  statedb.Identity
	Revoked   RevokedSet
	Telemetry *telemetry.Telemetry
	// Now is the clock for token expiry and certificate issue; nil means
	// time.Now.
	Now func() time.Time
	// Random is the source of machine ids and serials; nil means crypto/rand.
	Random io.Reader
	// HelloWait bounds the wait for a Connect stream's first message; 0
	// means [HelloWait]. Tests shorten it.
	HelloWait time.Duration
	// afterHello, set only by tests (export_test.go), runs after each Hello
	// transaction commits, before the stream is registered.
	afterHello func()
	// afterRegister, set only by tests, runs after the stream is registered.
	afterRegister func()
	// afterHeartbeat, set only by tests, runs after a stream's handler
	// finished with a Heartbeat wake, with the stream's Heartbeat count then.
	afterHeartbeat func(n uint64)
	// beforeRowClose, set only by tests, runs as a stream's handler is
	// about to close its connection row.
	beforeRowClose func()
	// Appliers are the later rows' parts of the Snapshot transaction, run in
	// order inside its writer job after the core and before ONLINE
	// (ingest.Applier); empty until plan rows 7 to 10.
	Appliers []ingest.Applier
	// OnSnapshotApplied, when set, is called on the stream's handler goroutine
	// after a Snapshot of the machine's current epoch committed: the
	// machine is ONLINE on that stream. It runs after the outbox's ready
	// seam, which sends the machine's pending commands. It must not block.
	OnSnapshotApplied func(machineID string, epoch uint64)
	// Watch publishes committed gateway changes (snapshots, events,
	// state changes, enrolls, pane summaries) to Watch subscribers: the
	// daemon wires each hook to a Fleet publish method. The zero value
	// publishes nothing.
	Watch WatchSink
	// EventAppliers are the later rows' parts of an Event's writer job
	// (ingest.EventApplier), run in order for an event the hub did not
	// already hold; empty until plan rows 7 to 10.
	EventAppliers []ingest.EventApplier
	// eventAcker, set only by tests, records the EventAcks the hub would
	// send instead of sending them; nil sends them on the stream.
	eventAcker eventAcker
	// arms, set only by tests, adds arm handlers to the product table.
	arms armTable
	// beforeSnapshot, set only by tests, runs in the Snapshot arm with the
	// stream's context, before its writer job.
	beforeSnapshot func(context.Context)
	// beforeMark, set only by tests, runs in housekeeping after it judged a
	// machine and before its MarkState job.
	beforeMark func(machineID string)
}

// NewServer is the agent listener (listen.agent): TLS 1.3 only, the
// certificate from Certs (swapped at run time without a restart), and an
// optional client certificate that must chain to the hub CA with client-auth
// usage and whose serial is not in Revoked, checked on fresh and resumed
// handshakes. It serves AgentService with requests capped at
// [MaxMessageBytes]: Enroll, RenewCert and Connect; Terminal and
// SessionLog answer Unimplemented until their rows land. Shutdown sends
// Bye{HUB_RESTART} on every live Connect stream. Start it with ServeTLS(ln, "", ""), which also offers HTTP/2 for
// gRPC. net/http's own error lines (a failed handshake, a handler panic) go
// to Telemetry as http.server_error events, never to stderr. To stop it,
// call Shutdown, then [Server.FlushAudit], then close the store.
func NewServer(opts ServerOptions) (*Server, error) {
	if opts.Certs == nil || opts.Store == nil || opts.Revoked == nil || opts.Telemetry == nil {
		return nil, errors.New("gateway: NewServer needs certificates, a store, the revoked set and telemetry")
	}
	certs, tel := opts.Certs, opts.Telemetry
	deps, err := newEnrollDeps(opts)
	if err != nil {
		return nil, err
	}
	cdeps, err := newConnectDeps(opts)
	if err != nil {
		return nil, err
	}
	cdeps.outbox, err = outbox.New(outbox.Options{Store: opts.Store, Streams: cdeps.streams, Telemetry: tel, Now: opts.Now})
	if err != nil {
		return nil, err
	}
	// The Snapshot's commit makes the machine's pending commands ready: the
	// outbox sends nothing before it (row 5 ruling, fork 1). ready refuses an
	// epoch that is not the live stream's, so a Snapshot handled after the
	// agent left sends nothing.
	userApplied := cdeps.onApplied
	cdeps.onApplied = func(machineID string, epoch uint64) {
		cdeps.ready(machineID, epoch)
		if userApplied != nil {
			userApplied(machineID, epoch)
		}
	}
	cdeps.watch = opts.Watch
	mux := http.NewServeMux()
	path, h := agentv1connect.NewAgentServiceHandler(&agentService{enrollDeps: deps, connectDeps: cdeps},
		connect.WithReadMaxBytes(MaxMessageBytes))
	mux.Handle(path, withPeerCert(h))
	handlers := newInflight()
	srv := &Server{deps: deps, connect: cdeps, handlers: handlers, Server: &http.Server{
		Handler: handlers.track(mux),
		TLSConfig: &tls.Config{
			MinVersion:       tls.VersionTLS13,
			GetCertificate:   certs.GetCertificate,
			ClientAuth:       tls.VerifyClientCertIfGiven,
			ClientCAs:        certs.auth.Pool(),
			VerifyConnection: verifyConnection(opts.Revoked, tel, deps.log),
		},
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       IdleTimeout,
		ErrorLog:          tel.HTTPErrorLog("agent"),
	}}
	// Shutdown (fleetd run's stop) says Bye{HUB_RESTART} on every live
	// Connect stream, whose handler then returns, so the stop does not wait
	// out its grace period (03n).
	srv.RegisterOnShutdown(cdeps.beginShutdown)
	cdeps.startOutbox()
	return srv, nil
}

// Server is the agent listener: the http.Server to serve and shut down,
// and the audit work to finish after it stops.
type Server struct {
	*http.Server
	deps    *enrollDeps
	connect *connectDeps
	// handlers counts running handlers for [Server.Shutdown].
	handlers *inflight
}

// ByeMachine ends the machine's live Connect stream with Bye{REVOKED},
// for RevokeMachine after commit (02g "Machine lifecycle: revoke and
// delete"). It reports whether the hub held a live stream; the stream's
// own teardown closes the connection row with EndRevoked.
func (s *Server) ByeMachine(machineID string) bool {
	return s.connect.streams.cancel(machineID, agentv1.ByeReason_BYE_REASON_REVOKED, statedb.EndRevoked)
}

// SetWatchSink wires committed gateway changes to Watch subscribers (the
// Watch ingest slice): the daemon calls it with the Fleet publish
// methods once both exist. It must run before the server serves.
func (s *Server) SetWatchSink(sink WatchSink) {
	s.connect.watch = sink
}

// InstanceID is the hub_instance_id this process sends on the agent wire
// (HelloAck): random per process start, shared with GetServerInfo so a new
// process id invalidates every Watch resume token (02k).
func (s *Server) InstanceID() string {
	return s.connect.instanceID
}

// FlushAudit writes the machine.enroll_failed summary of the minute in
// progress, if any refusal in it was suppressed, and waits for the commit
// (at most 5 s). It also stops the command sender and waits, within ctx,
// for it to end: it is the listener's last step before the store closes.
// Call it after Shutdown has returned, so no handler adds to the minute,
// and before the store closes. A second call writes nothing.
func (s *Server) FlushAudit(ctx context.Context) {
	s.deps.flushAudit(ctx)
	s.connect.stopOutbox(ctx)
}

// ReadHeaderTimeout bounds how long a peer may take to send request headers.
// There is no ReadTimeout: Connect, Terminal and SessionLog are long streams.
const ReadHeaderTimeout = 10 * time.Second

// IdleTimeout closes a connection with no request (HTTP/1.1) or no open
// stream (HTTP/2) for this long; an agent's Connect stream keeps its
// connection busy, so only abandoned connections are closed.
const IdleTimeout = 2 * time.Minute

func noopMeter() metric.Meter { return noop.NewMeterProvider().Meter(scope) }

func prefix(tel *telemetry.Telemetry) string {
	if tel == nil {
		return "fleet_hub"
	}
	return tel.MetricPrefix()
}
