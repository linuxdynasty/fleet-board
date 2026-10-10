package discovery

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"syscall"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/hub/discovery"

// Cert is the certificate the agent listener serves now.
type Cert interface {
	// Serves reports whether the served certificate names name.
	Serves(name string) bool
}

// Responder is the multicast socket layer, the only part that touches the
// network. Tests use a fake: the default test run sends no multicast.
type Responder interface {
	// CheckBind reports whether the mDNS group of family ("udp4" or
	// "udp6") can be joined on the named interface. The library hides a
	// failed family, so the hub checks each itself.
	CheckBind(iface, family string) error
	// Serve answers mDNS queries for r, on r.Iface, until stop is called.
	Serve(r Records) (stop func(), err error)
}

// Env holds the seams to the machine; the zero value is the real one.
type Env struct {
	// Responder nil means the hashicorp/mdns server ([NewResponder]).
	Responder Responder
	// Ifaces lists this machine's interfaces and their addresses; nil
	// means [Interfaces].
	Ifaces func() ([]Iface, error)
	// Route finds the default route's local addresses; nil means
	// [RouteSource].
	Route func() Route
}

// Options build an [Advertiser].
type Options struct {
	// Enabled is discovery.mdns.
	Enabled bool
	// Hostname is the machine's host name, as the server certificate's
	// names were made from it.
	Hostname string
	// Listener is the bound agent listener's address.
	Listener net.Addr
	// Cert is the served certificate, checked for the SRV target; nil
	// serves nothing.
	Cert Cert
	Env  Env
	// Telemetry may be nil: no signals.
	Telemetry *telemetry.Telemetry
	// meter replaces Telemetry's meter; a test sets it (export_test.go) to
	// make the registration fail.
	meter metric.Meter
}

// Advertiser advertises the hub as _fleetd._tcp while the agent listener
// serves. It has no goroutine of its own: Start and Stop are called from the
// one goroutine that owns the listeners. The library's receive goroutines
// leave shortly after Stop closes their sockets.
type Advertiser struct {
	opts    Options
	resp    Responder
	log     *slog.Logger
	total   metric.Int64Counter
	running metric.Int64UpDownCounter
	started bool
	stop    func()
	rec     Records
}

// New builds an Advertiser; nothing is sent until [Advertiser.Start]. It
// cannot fail: if the signals cannot be registered the advertiser runs
// without them and says so once, since a missing counter must not cost the
// hub its discovery.
func New(opts Options) *Advertiser {
	a := &Advertiser{opts: opts, resp: opts.Env.Responder, log: slog.New(slog.DiscardHandler)}
	if a.resp == nil {
		a.resp = NewResponder()
	}
	if a.opts.Env.Ifaces == nil {
		a.opts.Env.Ifaces = Interfaces
	}
	if a.opts.Env.Route == nil {
		a.opts.Env.Route = RouteSource
	}
	meter, prefix := noop.NewMeterProvider().Meter(scope), "fleet_hub"
	if opts.Telemetry != nil {
		a.log = opts.Telemetry.Logger.With("component", "discovery")
		meter, prefix = opts.Telemetry.Meter(scope), opts.Telemetry.MetricPrefix()
	}
	if opts.meter != nil {
		meter = opts.meter
	}
	var err1, err2 error
	a.total, err1 = meter.Int64Counter(prefix+"_discovery_advertise_total",
		metric.WithDescription("mDNS advertisements the hub tried at start, by result and, for a refusal, reason."))
	a.running, err2 = meter.Int64UpDownCounter(prefix+"_discovery_advertising",
		metric.WithDescription("1 while the hub advertises _fleetd._tcp over mDNS, else 0."))
	if err := errors.Join(err1, err2); err != nil {
		a.total, a.running = noop.Int64Counter{}, noop.Int64UpDownCounter{}
		a.log.Warn("discovery.advertise_failed", "step", "telemetry", "err_kind", errKind(err))
	}
	return a
}

// Start decides, once, whether to advertise, and does. It never fails the
// hub: a refusal or a failure is logged and counted once and the hub keeps
// serving (02j "Discovery"). Call it after the agent listener serves.
func (a *Advertiser) Start() {
	if a.started {
		return
	}
	a.started = true
	ctx := context.Background()
	a.running.Add(ctx, 0) // the series exists from the start
	if !a.opts.Enabled {
		a.log.Info("discovery.advertise_disabled", "reason", "config")
		a.count(ctx, "disabled", "")
		return
	}
	listener, err := netip.ParseAddrPort(addrString(a.opts.Listener))
	if err != nil {
		a.refuse(ctx, ReasonBadListener)
		return
	}
	// The cheap checks first: the name and the certificate, before any
	// socket is touched.
	target := ca.ServiceTarget(a.opts.Hostname)
	if target != "" && (a.opts.Cert == nil || !a.opts.Cert.Serves(target)) {
		a.refuse(ctx, ReasonCertMissingTarget)
		return
	}
	ifaces, err := a.opts.Env.Ifaces()
	if err != nil {
		a.fail(ctx, "addresses", err)
		return
	}
	rec, reason := Plan(a.opts.Hostname, listener, ifaces, a.opts.Env.Route())
	if reason != "" {
		a.refuse(ctx, reason)
		return
	}
	bound := 0
	for _, family := range []string{"udp4", "udp6"} {
		if err := a.resp.CheckBind(rec.Iface, family); err != nil {
			a.log.Warn("discovery.family_unavailable", "family", family, "err_kind", errKind(err))
			continue
		}
		bound++
	}
	if bound == 0 {
		a.fail(ctx, "bind", errors.New("no multicast family can bind"))
		return
	}
	stop, err := a.resp.Serve(rec)
	if err != nil {
		a.fail(ctx, "serve", err)
		return
	}
	a.stop, a.rec = stop, rec
	a.running.Add(ctx, 1)
	a.count(ctx, "started", "")
	a.log.Info("discovery.advertise_started", "iface", rec.Iface, "port", rec.Port, "addresses", len(rec.IPs), "families", bound)
}

// Advertised is what the hub is advertising now, or false.
func (a *Advertiser) Advertised() (Records, bool) { return a.rec, a.stop != nil }

// Stop ends the advertising; it is safe to call more than once and when
// nothing was advertised.
func (a *Advertiser) Stop() {
	if a.stop == nil {
		return
	}
	a.stop()
	a.stop, a.rec = nil, Records{}
	a.running.Add(context.Background(), -1)
	a.log.Info("discovery.advertise_stopped")
}

func (a *Advertiser) count(ctx context.Context, result string, reason Reason) {
	attrs := []attribute.KeyValue{attribute.String("result", result)}
	if reason != "" {
		attrs = append(attrs, attribute.String("reason", string(reason)))
	}
	a.total.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func (a *Advertiser) refuse(ctx context.Context, reason Reason) {
	a.count(ctx, "refused", reason)
	a.log.Warn("discovery.advertise_refused", "reason", string(reason))
}

func (a *Advertiser) fail(ctx context.Context, step string, err error) {
	a.count(ctx, "failed", "")
	a.log.Warn("discovery.advertise_failed", "step", step, "err_kind", errKind(err))
}

func addrString(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}

// errKind is the closed err_kind class of 05b; never the error text.
func errKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.EADDRINUSE), errors.Is(err, syscall.EACCES):
		return "refused"
	default:
		return "io"
	}
}
