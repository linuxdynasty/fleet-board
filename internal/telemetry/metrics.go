package telemetry

import (
	"context"
	"net"
	"net/http"
	"net/netip"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// MetricsHandler serves the Prometheus text format (05b section 4). It admits
// a request whose TCP peer is loopback, or one that allow accepts (the caller
// checks an ADMIN API token there); allow may be nil. Proxy headers such as
// X-Forwarded-For are ignored. A refused request gets 401 with no body and
// counts in <prefix>_auth_failures_total{reason="metrics"}.
func (t *Telemetry) MetricsHandler(allow func(*http.Request) bool) http.Handler {
	serve := promhttp.HandlerFor(t.registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackPeer(r.RemoteAddr) && (allow == nil || !allow(r)) {
			t.CountAuthFailure(r.Context(), ReasonMetrics)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		serve.ServeHTTP(w, r)
	})
}

// AuthReason is the closed label set of <prefix>_auth_failures_total.
type AuthReason string

// The refusals counted so far; sign-in, token and device reasons arrive with
// their rows.
const (
	ReasonMetrics AuthReason = "metrics"  // /metrics from a non-loopback peer without an allowed credential
	ReasonHost    AuthReason = "host"     // Host header not allowed (02f, HOST_NOT_ALLOWED)
	ReasonOrigin  AuthReason = "origin"   // Origin header not allowed (02f, ORIGIN_NOT_ALLOWED)
	ReasonPeerUID AuthReason = "peer_uid" // admin socket peer is another OS user, or its credentials are unreadable (02f)
)

// CountAuthFailure adds one refused request to <prefix>_auth_failures_total.
func (t *Telemetry) CountAuthFailure(ctx context.Context, reason AuthReason) {
	t.refused.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", string(reason))))
}

func loopbackPeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
