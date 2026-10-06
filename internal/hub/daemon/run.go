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
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// RenewEvery is how often run checks the server certificate.
const RenewEvery = time.Hour

// shutdownGrace bounds how long a stop waits for open requests.
const shutdownGrace = 10 * time.Second

// Run is `fleetd run`: it loads fleetd.yaml, checks the state folder, takes
// fleetd.lock, opens the existing state.db (never creating one) and its
// hub_identity row, makes sure the server certificate is current, then
// serves the agent listener, the API listener and the optional metrics
// listener until ctx ends (SIGTERM in the binary), and shuts down cleanly. A
// refusal to start is a *[StartError].
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
	certs, err := newCerts(ctx, cfg, opts, st, id, s)
	if err != nil {
		return s.fail(ReasonCert, err)
	}
	return serve(ctx, cfg, opts, certs, st, id, s)
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
func serve(ctx context.Context, cfg *config.Config, opts Options, certs *gateway.Certs, st *store.Store,
	id statedb.Identity, s *session,
) error {
	ls, err := bind(cfg, certs, s)
	if err != nil {
		return s.fail(ReasonListen, err)
	}
	addrs := Addrs{Agent: ls[0].ln.Addr(), API: ls[1].ln.Addr()}
	if len(ls) > 2 {
		addrs.Metrics = ls[2].ln.Addr()
	}
	admin, err := bindAdmin(cfg, opts, st, id, s)
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
	go func() { defer wg.Done(); certs.Run(ctx, ticks) }()

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
func bind(cfg *config.Config, certs *gateway.Certs, s *session) ([]listener, error) {
	type want struct{ name, addr string }
	wants := []want{{"agent", cfg.Listen.Agent}, {"api", cfg.Listen.API}}
	if cfg.Listen.Metrics != "" {
		wants = append(wants, want{"metrics", cfg.Listen.Metrics})
	}
	var ls []listener
	for _, w := range wants {
		ln, err := net.Listen("tcp", w.addr)
		if err != nil {
			for _, l := range ls {
				_ = l.ln.Close() // the bind error is the one to report
			}
			return nil, fmt.Errorf("daemon: listen.%s %s: %w", w.name, w.addr, err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		l := listener{name: w.name, ln: ln, tls: w.name == "agent"}
		if l.tls {
			l.srv = gateway.NewServer(certs, s.tel)
		} else {
			l.srv = api.NewServer(s.tel, w.name, port)
		}
		ls = append(ls, l)
		s.tel.Logger.Info("hub.listener_opened", "listener", w.name, "port", port)
	}
	return ls, nil
}
