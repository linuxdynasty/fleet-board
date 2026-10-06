package gateway

import (
	"crypto/tls"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// NewServer is the agent listener (listen.agent): TLS 1.3 only, the
// certificate from certs (swapped at run time without a restart), and an
// optional client certificate that must chain to the hub CA. Enroll needs
// none; checking the certificate per RPC and revocation arrive with plan rows
// 3 and 5. It serves AgentService, and every method answers Unimplemented
// until those rows land. Start it with ServeTLS(ln, "", ""), which also
// offers HTTP/2 for gRPC. net/http's own error lines (a failed handshake, a
// handler panic) go to tel as http.server_error events, never to stderr; tel
// must not be nil.
func NewServer(certs *Certs, tel *telemetry.Telemetry) *http.Server {
	mux := http.NewServeMux()
	mux.Handle(agentv1connect.NewAgentServiceHandler(agentv1connect.UnimplementedAgentServiceHandler{}))
	return &http.Server{
		Handler: mux,
		TLSConfig: &tls.Config{
			MinVersion:     tls.VersionTLS13,
			GetCertificate: certs.GetCertificate,
			ClientAuth:     tls.VerifyClientCertIfGiven,
			ClientCAs:      certs.auth.Pool(),
		},
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       IdleTimeout,
		ErrorLog:          tel.HTTPErrorLog("agent"),
	}
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
