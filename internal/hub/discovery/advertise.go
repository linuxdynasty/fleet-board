package discovery

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"

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

// RecheckEvery is how often fleetd re-reads the machine's addresses and
// re-advertises when they changed (defaults.md "mDNS re-advertise check"):
// the rate limit on re-serving, so a flapping network re-serves at most
// once a minute.
const RecheckEvery = 60 * time.Second

// Advertiser advertises the hub as _fleetd._tcp while the agent listener
// serves. It has no goroutine of its own: Start and Stop are called from the
// one goroutine that owns the listeners, Recheck from the recheck tick's.
// The library's receive goroutines leave shortly after Stop closes their
// sockets.
type Advertiser struct {
	mu      sync.Mutex
	opts    Options
	resp    Responder
	log     *slog.Logger
	total   metric.Int64Counter
	running metric.Int64UpDownCounter
	started bool
	stopped bool
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
	// Keep whichever registered: a counter that fails alone costs the counts,
	// not the gauge.
	if err1 != nil {
		a.total = noop.Int64Counter{}
	}
	if err2 != nil {
		a.running = noop.Int64UpDownCounter{}
	}
	if err := errors.Join(err1, err2); err != nil {
		a.log.Warn("discovery.advertise_failed", "step", "telemetry", "err_kind", errKind(err))
	}
	return a
}

// Start decides, once, whether to advertise, and does. It never fails the
// hub: a refusal or a failure is logged and counted once and the hub keeps
// serving (02j "Discovery"). Call it after the agent listener serves.
func (a *Advertiser) Start() {
	a.mu.Lock()
	defer a.mu.Unlock()
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
	if rec, ok := a.check(ctx); ok {
		if families, ok := a.serve(ctx, rec); ok {
			a.count(ctx, "started", "")
			a.log.Info("discovery.advertise_started", "iface", rec.Iface, "port", rec.Port,
				"addresses", len(rec.IPs), "families", families)
		}
	}
}

// Recheck re-reads the machine's addresses and re-advertises when they no
// longer match what is served (02j "Discovery"): a hub that moved networks
// stops advertising the old addresses and serves the new ones. Nothing
// changes when the records match; a hub that never started (no network at
// boot, a refusal since lifted) starts. It never fails the hub: a recheck
// that cannot plan or serve keeps what is served now, still counted and
// logged. The recheck tick's cadence (RecheckEvery) is the rate limit, so
// a flapping network re-serves at most once a tick.
func (a *Advertiser) Recheck() {
	a.mu.Lock()
	defer a.mu.Unlock()
	// A queued tick racing shutdown serves nothing: Stop is final.
	if !a.started || a.stopped || !a.opts.Enabled {
		return
	}
	ctx := context.Background()
	rec, ok := a.check(ctx)
	if !ok {
		return
	}
	if a.stop != nil && equalRecords(a.rec, rec) {
		return
	}
	refreshed := a.stop != nil
	if refreshed {
		a.stop()
		a.stop, a.rec = nil, Records{}
		a.running.Add(ctx, -1)
		a.log.Info("discovery.advertise_stopped")
	}
	families, ok := a.serve(ctx, rec)
	if !ok {
		return
	}
	fields := []any{"iface", rec.Iface, "port", rec.Port, "addresses", len(rec.IPs), "families", families}
	if refreshed {
		a.count(ctx, "refreshed", "")
		a.log.Info("discovery.advertise_refreshed", fields...)
	} else {
		a.count(ctx, "started", "")
		a.log.Info("discovery.advertise_started", fields...)
	}
}

// check plans the records to advertise, refusing or failing (logged and
// counted) when there is nothing to serve. The cheap checks run first: the
// name and the certificate, before any socket is touched.
func (a *Advertiser) check(ctx context.Context) (Records, bool) {
	listener, err := netip.ParseAddrPort(addrString(a.opts.Listener))
	if err != nil {
		a.refuse(ctx, ReasonBadListener)
		return Records{}, false
	}
	target := ca.ServiceTarget(a.opts.Hostname)
	if target != "" && (a.opts.Cert == nil || !a.opts.Cert.Serves(target)) {
		a.refuse(ctx, ReasonCertMissingTarget)
		return Records{}, false
	}
	ifaces, err := a.opts.Env.Ifaces()
	if err != nil {
		a.fail(ctx, "addresses", err)
		return Records{}, false
	}
	rec, reason := Plan(a.opts.Hostname, listener, ifaces, a.opts.Env.Route())
	if reason != "" {
		a.refuse(ctx, reason)
		return Records{}, false
	}
	return rec, true
}

// serve binds the responder and serves rec: families bound and true, with
// the serve running. A bind or serve failure is logged and counted and
// serves nothing; the caller counts the result and logs the start line, and
// keeps or stops the old serve.
func (a *Advertiser) serve(ctx context.Context, rec Records) (families int, ok bool) {
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
		return 0, false
	}
	stop, err := a.resp.Serve(rec)
	if err != nil {
		a.fail(ctx, "serve", err)
		return 0, false
	}
	a.stop, a.rec = stop, rec
	a.running.Add(ctx, 1)
	return bound, true
}

// equalRecords is whether Plan would serve the same answer twice: every
// field the responder sends, in order.
func equalRecords(a, b Records) bool {
	if a.Instance != b.Instance || a.Service != b.Service || a.Domain != b.Domain ||
		a.Target != b.Target || a.Port != b.Port || a.Iface != b.Iface || !slices.Equal(a.TXT, b.TXT) ||
		len(a.IPs) != len(b.IPs) {
		return false
	}
	for i := range a.IPs {
		if !a.IPs[i].Equal(b.IPs[i]) {
			return false
		}
	}
	return true
}

// Advertised is what the hub is advertising now, or false.
func (a *Advertiser) Advertised() (Records, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rec, a.stop != nil
}

// Stop ends the advertising; it is safe to call more than once and when
// nothing was advertised.
func (a *Advertiser) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
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
