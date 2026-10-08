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
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
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
	certs, err := newCerts(ctx, cfg, opts, st, id, s)
	if err != nil {
		return s.fail(ReasonCert, err)
	}
	return serve(ctx, cfg, opts, hubDeps{certs: certs, st: st, id: id, revoked: revoked}, s)
}

// hubDeps is what the listeners share once the store is open.
type hubDeps struct {
	certs   *gateway.Certs
	st      *store.Store
	id      statedb.Identity
	revoked *revocation.List
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

func newCerts(ctx context.Context, cfg *config.Config, opts Options, st *store.Store, id statedb.Identity, s *session) (*gateway.Certs, error) {
	names, err := opts.names(cfg, ca.DefaultNames)
	if err != nil {
		return nil, err
	}
	certs, err := gateway.NewCerts(gateway.CertsOptions{Store: st, Identity: id, Names: names, Now: opts.Now, Telemetry: s.tel})
	if err != nil {
		return nil, err
	}
	return certs, certs.Ensure(ctx)
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

// listener is one bound address and the server for it.
type listener struct {
	name string
	ln   net.Listener
	srv  *http.Server
	tls  bool
}

// serve binds every listener, serves until ctx ends or one fails, then shuts
// them all down (which removes admin.sock) and waits for every goroutine it
// started.
func serve(ctx context.Context, cfg *config.Config, opts Options, d hubDeps, s *session) error {
	ls, err := bind(ctx, cfg, opts, d, s)
	if err != nil {
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
	shutdown, done := context.WithTimeout(context.Background(), shutdownGrace)
	defer done()
	for _, l := range ls {
		if err := l.srv.Shutdown(shutdown); err != nil {
			// Past the grace period: drop the connections still open.
			serveErr = errors.Join(serveErr, fmt.Errorf("daemon: stop %s listener: %w", l.name, err), l.srv.Close())
		}
	}
	wg.Wait()
	s.tel.Logger.Info("hub.stopped")
	return serveErr
}

// bind opens the agent, API and (when set) metrics listeners, in that order.
func bind(ctx context.Context, cfg *config.Config, opts Options, d hubDeps, s *session) ([]listener, error) {
	agent, err := gateway.NewServer(gateway.ServerOptions{
		Certs: d.certs, Store: d.st, Identity: d.id, Revoked: d.revoked, Telemetry: s.tel, Now: opts.Now,
	})
	if err != nil {
		return nil, err
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
			return nil, fmt.Errorf("daemon: listen.%s %s: %w", w.name, w.addr, err)
		}
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			_ = ln.Close()
			closeAll()
			return nil, fmt.Errorf("daemon: listen.%s %s: not a TCP address: %v", w.name, w.addr, ln.Addr())
		}
		port := addr.Port
		l := listener{name: w.name, ln: ln, tls: w.name == "agent"}
		if l.tls {
			l.srv = agent
		} else {
			l.srv = api.NewServer(s.tel, w.name, port)
		}
		ls = append(ls, l)
		s.tel.Logger.Info("hub.listener_opened", "listener", w.name, "port", port)
	}
	return ls, nil
}
