package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/discovery"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/housekeeping"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// RenewEvery is how often run checks the server certificate.
const RenewEvery = time.Hour

// shutdownGrace bounds how long a stop waits for open requests.
const shutdownGrace = 10 * time.Second

// Run is `fleetd run`: it loads fleetd.yaml, checks the state folder, takes
// fleetd.lock, opens the existing state.db (never creating one) and its
// hub_identity row, loads the revocation list (revoked-serials reconciled
// with state.db), makes sure the server certificate is current, then serves
// the agent listener (Enroll, refusing revoked serials), the API listener
// and the optional metrics listener until ctx ends (SIGTERM in the binary),
// and shuts down cleanly. A refusal to start is a *[StartError].
func Run(ctx context.Context, opts Options) (err error) {
	s, err := newSession("run", opts)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.close()) }()

	cfg, err := config.Load(opts.ConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("%s does not exist; run fleetd init first: %w", opts.ConfigPath, err)
	}
	if err != nil {
		return s.fail(ReasonConfig, err)
	}
	if err := statedb.CheckDir(cfg.StateDir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			err = fmt.Errorf("no state folder %s; run fleetd init first: %w", cfg.StateDir, err)
		}
		return s.fail(dirReason(err), err)
	}
	l, err := lock.Acquire(filepath.Join(cfg.StateDir, LockName))
	if err != nil {
		return s.fail(lockReason(err), fmt.Errorf("%s: %w", cfg.StateDir, err))
	}
	defer func() { err = errors.Join(err, l.Release()) }()
	if err := s.removeLeftovers(cfg.StateDir); err != nil {
		return s.fail(ReasonStore, err)
	}

	st, err := statedb.Open(ctx, cfg.StateDir, s.tel)
	if err != nil {
		return s.fail(openReason(err), openMessage(cfg.StateDir, err))
	}
	defer func() { err = errors.Join(err, st.Close()) }()
	id, err := statedb.ReadIdentity(ctx, st)
	if errors.Is(err, statedb.ErrNoIdentity) {
		return s.fail(ReasonIdentityMissing, fmt.Errorf("%s has no hub identity; it was not made by fleetd init: %w",
			statedb.Path(cfg.StateDir), err))
	}
	if err != nil {
		return s.fail(ReasonStore, err)
	}
	revoked, err := revocation.Load(ctx, revocation.Options{Dir: cfg.StateDir, Store: st, Now: opts.Now, Logger: s.tel.Logger})
	if err != nil {
		return s.fail(revocationReason(err), err)
	}
	certs, host, err := newCerts(ctx, cfg, opts, st, id, s)
	if err != nil {
		return s.fail(ReasonCert, err)
	}
	nightly, err := backup.NewSchedule(backup.ScheduleOptions{StateDir: cfg.StateDir, Now: opts.Now, Telemetry: s.tel})
	if err != nil {
		return s.fail(ReasonStore, err)
	}
	defer func() { err = errors.Join(err, nightly.Close()) }()
	swept, err := sweep(ctx, st, opts, s)
	if err != nil {
		return s.fail(ReasonStore, err)
	}
	return serve(ctx, cfg, opts, hubDeps{
		certs: certs, hostname: host, st: st, id: id, revoked: revoked, nightly: nightly, swept: swept.Changes,
	}, s)
}

// sweep is the hub-start pass (03n "Hub start"), before any listener
// serves, so no Hello can race it: rows left live are closed as
// hub_restart and every ONLINE machine becomes STALE.
func sweep(ctx context.Context, st *store.Store, opts Options, s *session) (statedb.Swept, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	out, err := statedb.StartSweep(ctx, st, now().UTC())
	if err != nil {
		return statedb.Swept{}, fmt.Errorf("daemon: start sweep: %w", err)
	}
	for _, c := range out.Changes {
		s.tel.Logger.InfoContext(ctx, "machine.state_changed", "machine_id", c.MachineID, "from", c.From, "to", c.To,
			"reason", c.Reason)
	}
	s.tel.Logger.InfoContext(ctx, "hub.start_sweep", "stale", len(out.Changes), "rows_closed", out.RowsClosed)
	return out, nil
}

// hubDeps is what the listeners and jobs share once the store is open.
type hubDeps struct {
	certs *gateway.Certs
	// hostname is what the server certificate's names were made from; the
	// mDNS SRV target is made from it too.
	hostname string
	st       *store.Store
	id       statedb.Identity
	revoked  *revocation.List
	nightly  *backup.Schedule
	// swept are the start sweep's changes, for the agent listener's
	// fleet_hub_machine_state.
	swept []statedb.StateChange
}

// revocationReason: a loose revoked-serials is mode_loose like any other
// file in the state folder; any other failure to load the list refuses the
// start as revocations_invalid, so a hub never serves without it.
func revocationReason(err error) Reason {
	if errors.Is(err, statedb.ErrLooseMode) {
		return ReasonLooseMode
	}
	return ReasonRevocations
}

func newCerts(ctx context.Context, cfg *config.Config, opts Options, st *store.Store, id statedb.Identity, s *session) (*gateway.Certs, string, error) {
	names, host, err := opts.serverNames(cfg, id.HubAddresses)
	if err != nil {
		return nil, "", err
	}
	certs, err := gateway.NewCerts(gateway.CertsOptions{Store: st, Identity: id, Names: names, Now: opts.Now, Telemetry: s.tel})
	if err != nil {
		return nil, "", err
	}
	return certs, host, certs.Ensure(ctx)
}

func openReason(err error) Reason {
	switch {
	case errors.Is(err, statedb.ErrLooseMode):
		return ReasonLooseMode
	case errors.Is(err, store.ErrMissing):
		return ReasonStoreMissing
	default:
		return ReasonStore
	}
}

func openMessage(dir string, err error) error {
	if errors.Is(err, store.ErrMissing) {
		return fmt.Errorf("%s does not exist; run fleetd init first: %w", statedb.Path(dir), err)
	}
	return err
}

// server is what serve runs on a listener: an *http.Server, or the agent
// listener's *gateway.Server, whose Shutdown also waits for its running
// handlers (each Connect stream's row close) before the store closes.
type server interface {
	Serve(ln net.Listener) error
	ServeTLS(ln net.Listener, certFile, keyFile string) error
	Shutdown(ctx context.Context) error
	Close() error
}

// listener is one bound address and the server for it.
type listener struct {
	name string
	ln   net.Listener
	srv  server
	tls  bool
	// flush, when set, finishes the server's work after Shutdown and before
	// the store closes (the agent listener's audit summary).
	flush func(context.Context)
}

// serve binds every listener, serves until ctx ends or one fails, then shuts
// them all down (which removes admin.sock) and waits for every goroutine it
// started.
func serve(ctx context.Context, cfg *config.Config, opts Options, d hubDeps, s *session) error {
	ls, agent, err := bind(ctx, cfg, opts, d, s)
	if err != nil {
		return s.fail(ReasonListen, err)
	}
	agent.RecordStates(ctx, d.swept)
	hk, err := housekeeping.New(housekeeping.Options{Now: opts.Now, Telemetry: s.tel}, housekeeping.Job{
		Name: "machine_state", Every: housekeeping.TickEvery, Run: agent.Housekeep,
	}, housekeeping.Job{
		Name: "outbox_expiry", Every: housekeeping.TickEvery, Run: agent.Outbox().ExpireDue,
	}, housekeeping.Job{
		Name: "outbox_prune", Every: outbox.PruneEvery, Run: agent.Outbox().Prune,
	})
	if err != nil {
		for _, l := range ls {
			_ = l.ln.Close() // the housekeeping error is the one to report
		}
		return s.fail(ReasonListen, err)
	}
	addrs := Addrs{Agent: ls[0].ln.Addr(), API: ls[1].ln.Addr()}
	if len(ls) > 2 {
		addrs.Metrics = ls[2].ln.Addr()
	}
	admin, err := bindAdmin(cfg, opts, d.st, d.id, s)
	if err != nil {
		for _, l := range ls {
			_ = l.ln.Close() // the bind error is the one to report
		}
		return s.fail(ReasonListen, err)
	}
	adv := discovery.New(discovery.Options{
		Enabled: cfg.Discovery.MDNS, Hostname: d.hostname, Listener: ls[0].ln.Addr(),
		Cert: d.certs, Env: opts.Discovery, Telemetry: s.tel,
	})
	ls = append(ls, admin)
	addrs.Admin = admin.ln.Addr()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	failed := make(chan error, len(ls))
	for _, l := range ls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if l.tls {
				err = l.srv.ServeTLS(l.ln, "", "")
			} else {
				err = l.srv.Serve(l.ln)
			}
			if !errors.Is(err, http.ErrServerClosed) {
				failed <- fmt.Errorf("daemon: %s listener: %w", l.name, err)
			}
		}()
	}
	ticks := opts.RenewTicks
	if ticks == nil {
		t := time.NewTicker(RenewEvery)
		defer t.Stop()
		ticks = t.C
	}
	wg.Add(1)
	go func() { defer wg.Done(); d.certs.Run(ctx, ticks) }()
	backupTicks := opts.BackupTicks
	if backupTicks == nil {
		t := time.NewTicker(backup.CheckEvery)
		defer t.Stop()
		backupTicks = t.C
	}
	wg.Add(1)
	go func() { defer wg.Done(); d.nightly.Run(ctx, backupTicks) }()
	hkTicks := opts.HousekeepingTicks
	if hkTicks == nil {
		t := time.NewTicker(housekeeping.TickEvery)
		defer t.Stop()
		hkTicks = t.C
	}
	wg.Add(1)
	go func() { defer wg.Done(); hk.Run(ctx, hkTicks) }()

	// Advertise only now that the agent listener serves: an agent that
	// follows the answer finds it. It never fails the hub (02j "Discovery").
	adv.Start()
	s.tel.Logger.Info("hub.started", "mode", cfg.Mode, "listeners", len(ls))
	if opts.Ready != nil {
		opts.Ready(addrs)
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-failed:
		s.tel.Logger.Error("hub.listener_failed", "err_kind", errKind(serveErr))
	}
	cancel()
	adv.Stop()
	serveErr = errors.Join(serveErr, stopAll(ls))
	wg.Wait()
	for _, l := range ls {
		if l.flush != nil {
			l.flush(context.WithoutCancel(ctx))
		}
	}
	s.tel.Logger.Info("hub.stopped")
	return serveErr
}

// stopAll shuts every listener down at the same time, each within the one
// shutdownGrace: the agent listener's wait for its Connect handlers does
// not eat the other listeners' grace. A listener still busy past it has
// its open connections dropped.
func stopAll(ls []listener) error {
	shutdown, done := context.WithTimeout(context.Background(), shutdownGrace)
	defer done()
	errs := make([]error, len(ls))
	var wg sync.WaitGroup
	for i, l := range ls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.srv.Shutdown(shutdown); err != nil {
				errs[i] = errors.Join(fmt.Errorf("daemon: stop %s listener: %w", l.name, err), l.srv.Close())
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// bind opens the agent, API and (when set) metrics listeners, in that
// order, and returns the agent listener's server too.
func bind(ctx context.Context, cfg *config.Config, opts Options, d hubDeps, s *session) ([]listener, *gateway.Server, error) {
	agent, err := gateway.NewServer(gateway.ServerOptions{
		Certs: d.certs, Store: d.st, Identity: d.id, Revoked: d.revoked, Telemetry: s.tel, Now: opts.Now,
	})
	if err != nil {
		return nil, nil, err
	}
	type want struct{ name, addr string }
	wants := []want{{"agent", cfg.Listen.Agent}, {"api", cfg.Listen.API}}
	if cfg.Listen.Metrics != "" {
		wants = append(wants, want{"metrics", cfg.Listen.Metrics})
	}
	var ls []listener
	closeAll := func() {
		for _, l := range ls {
			_ = l.ln.Close() // the bind error is the one to report
		}
	}
	var lc net.ListenConfig
	for _, w := range wants {
		ln, err := lc.Listen(ctx, "tcp", w.addr)
		if err != nil {
			closeAll()
			return nil, nil, fmt.Errorf("daemon: listen.%s %s: %w", w.name, w.addr, err)
		}
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			_ = ln.Close()
			closeAll()
			return nil, nil, fmt.Errorf("daemon: listen.%s %s: not a TCP address: %v", w.name, w.addr, ln.Addr())
		}
		port := addr.Port
		l := listener{name: w.name, ln: ln, tls: w.name == "agent"}
		if l.tls {
			l.srv, l.flush = agent, agent.FlushAudit
		} else {
			l.srv = api.NewServer(s.tel, w.name, port)
		}
		if opts.afterBind != nil {
			opts.afterBind(l.name, l.srv)
		}
		ls = append(ls, l)
		s.tel.Logger.Info("hub.listener_opened", "listener", w.name, "port", port)
	}
	return ls, agent, nil
}
