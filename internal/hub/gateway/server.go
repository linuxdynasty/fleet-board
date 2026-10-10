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

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
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
}

// NewServer is the agent listener (listen.agent): TLS 1.3 only, the
// certificate from Certs (swapped at run time without a restart), and an
// optional client certificate that must chain to the hub CA with client-auth
// usage and whose serial is not in Revoked, checked on fresh and resumed
// handshakes. It serves AgentService with requests capped at
// [MaxMessageBytes]; every method but Enroll and RenewCert answers
// Unimplemented until its row lands. Start it with ServeTLS(ln, "", ""), which also offers HTTP/2 for
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
	mux := http.NewServeMux()
	path, h := agentv1connect.NewAgentServiceHandler(&agentService{enrollDeps: deps}, connect.WithReadMaxBytes(MaxMessageBytes))
	mux.Handle(path, withPeerCert(h))
	srv := &Server{deps: deps, Server: &http.Server{
		Handler: mux,
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
	return srv, nil
}

// Server is the agent listener: the http.Server to serve and shut down,
// and the audit work to finish after it stops.
type Server struct {
	*http.Server
	deps *enrollDeps
}

// FlushAudit writes the machine.enroll_failed summary of the minute in
// progress, if any refusal in it was suppressed, and waits for the commit
// (at most 5 s). Call it after Shutdown has returned, so no handler adds to
// the minute, and before the store closes. A second call writes nothing.
func (s *Server) FlushAudit(ctx context.Context) { s.deps.flushAudit(ctx) }

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
