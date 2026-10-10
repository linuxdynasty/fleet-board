package gateway

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/hub/gateway"

// CertsOptions build [Certs].
type CertsOptions struct {
	Store *store.Store
	// Identity is the hub_identity row read at start.
	Identity statedb.Identity
	// Names is the SAN list the certificate must carry: ca.AgentNames,
	// which is tls.agent_names (or the default names when that is empty)
	// plus the mDNS SRV target.
	Names []string
	// Now is the clock; nil means time.Now.
	Now       func() time.Time
	Telemetry *telemetry.Telemetry
}

// Certs serves the agent listener's certificate from an atomic pointer and
// re-issues it, signed by the stored CA, when the names change or two thirds
// of its life have passed. Each re-issue is one writer transaction; the
// served certificate changes only after it commits.
type Certs struct {
	served atomic.Pointer[tls.Certificate]
	auth   *ca.Authority
	names  []string
	store  *store.Store
	now    func() time.Time
	log    *slog.Logger
	issues metric.Int64Counter
}

// NewCerts loads the stored server certificate; call [Certs.Ensure] before
// serving.
func NewCerts(opts CertsOptions) (*Certs, error) {
	auth, err := ca.Parse(opts.Identity.CACert, opts.Identity.CAKey)
	if err != nil {
		return nil, err
	}
	c := &Certs{
		auth:  auth,
		names: slices.Clone(opts.Names),
		store: opts.Store,
		now:   opts.Now,
		log:   slog.New(slog.DiscardHandler),
	}
	if c.now == nil {
		c.now = time.Now
	}
	meter := noopMeter()
	if opts.Telemetry != nil {
		c.log = opts.Telemetry.Logger.With("component", "gateway")
		meter = opts.Telemetry.Meter(scope)
	}
	c.issues, err = meter.Int64Counter(prefix(opts.Telemetry)+"_server_cert_issues_total",
		metric.WithDescription("Agent listener certificates re-issued, by reason and result."))
	if err != nil {
		return nil, fmt.Errorf("gateway: counter: %w", err)
	}
	cert, err := tlsCert(opts.Identity.ServerCert, opts.Identity.ServerKey, auth.Cert.Raw)
	if err != nil {
		return nil, err
	}
	c.served.Store(cert)
	return c, nil
}

// tlsCert is the served chain: the server certificate, then the CA. An
// enrolling agent knows only the CA's SHA-256 (02g step 2) and finds the
// root in the chain by it.
func tlsCert(certDER, keyDER, caDER []byte) (*tls.Certificate, error) {
	leaf, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("gateway: parse server certificate: %w", err)
	}
	key, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, fmt.Errorf("gateway: parse server key: %w", err)
	}
	return &tls.Certificate{Certificate: [][]byte{certDER, caDER}, PrivateKey: key, Leaf: leaf}, nil
}

// GetCertificate is tls.Config.GetCertificate: the current certificate.
func (c *Certs) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return c.served.Load(), nil
}

// Served is the DER of the certificate served now.
func (c *Certs) Served() []byte { return c.served.Load().Leaf.Raw }

// Serves reports whether the certificate served now names name (a DNS name or
// an IP address). The mDNS advertiser asks it before it advertises the SRV
// target: after a failed re-issue the old certificate still serves.
func (c *Certs) Serves(name string) bool {
	return c.served.Load().Leaf.VerifyHostname(name) == nil
}

// Ensure re-issues the certificate when its names differ from the wanted
// ones or it is at two thirds of its life, stores it, then serves it. On a
// failure the old certificate stays in service and in state.db.
func (c *Certs) Ensure(ctx context.Context) error {
	cur := c.served.Load()
	var reason string
	switch {
	case !ca.SameNames(cur.Leaf, c.names):
		reason = "names_changed"
	case ca.NeedsRenewal(cur.Leaf, c.now()):
		reason = "renewal"
	default:
		return nil
	}
	next, err := c.reissue(ctx, cur)
	if err != nil {
		c.issues.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason), attribute.String("result", "failed")))
		c.log.Error("tls.server_cert_issue_failed", "reason", reason, "err_kind", errKind(err),
			"not_after", cur.Leaf.NotAfter.UTC().Format(time.RFC3339))
		return err
	}
	c.served.Store(next)
	c.issues.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason), attribute.String("result", "ok")))
	c.log.Info("tls.server_cert_issued", "reason", reason, "names", len(c.names),
		"not_after", next.Leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}

func (c *Certs) reissue(ctx context.Context, cur *tls.Certificate) (*tls.Certificate, error) {
	sc, err := c.auth.IssueServer(c.names, c.now(), rand.Reader)
	if err != nil {
		return nil, err
	}
	next, err := tlsCert(sc.CertDER, sc.KeyDER, c.auth.Cert.Raw)
	if err != nil {
		return nil, err
	}
	if err := statedb.ReplaceServerCert(ctx, c.store, cur.Leaf.Raw, sc.CertDER, sc.KeyDER, sc.Names); err != nil {
		return nil, fmt.Errorf("gateway: store the new server certificate: %w", err)
	}
	return next, nil
}

// Run calls Ensure on every tick (hourly in fleetd run) until ctx ends. A
// failure is already logged and counted by Ensure; the next tick retries.
func (c *Certs) Run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			_ = c.Ensure(ctx)
		}
	}
}

// errKind is the closed err_kind class of 05b; never the error text.
func errKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, statedb.ErrServerCertChanged):
		return "constraint"
	default:
		return "io"
	}
}
