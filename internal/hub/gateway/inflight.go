package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

// inflight counts the agent listener's running handlers. http.Server's
// Shutdown waits for connections, not handlers: once an agent that read
// its Bye{HUB_RESTART} drops the connection, Shutdown returns while that
// stream's handler may still be closing its connection row, and the store
// would close under it (the row then stays live and the next Hello closes
// it as displaced, not hub_restart). [Server.Shutdown] waits on this count.
type inflight struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // closed while n is 0
}

func newInflight() *inflight {
	c := make(chan struct{})
	close(c)
	return &inflight{idle: c}
}

// track counts next's calls while they run.
func (f *inflight) track(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.add()
		defer f.done()
		next.ServeHTTP(w, r)
	})
}

func (f *inflight) add() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == 0 {
		f.idle = make(chan struct{})
	}
	f.n++
}

func (f *inflight) done() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n--
	if f.n == 0 {
		close(f.idle)
	}
}

// wait blocks until no handler runs or ctx ends; then it reports how many
// still run.
func (f *inflight) wait(ctx context.Context) (int, error) {
	f.mu.Lock()
	idle := f.idle
	f.mu.Unlock()
	select {
	case <-idle:
		return 0, nil
	case <-ctx.Done():
	}
	// Both cases may have been ready: no handler left is no timeout.
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n == 0 {
		return 0, nil
	}
	return f.n, ctx.Err()
}

// Shutdown is http.Server.Shutdown (every Connect stream gets
// Bye{HUB_RESTART}, the listener closes, open connections drain), then a
// wait, bounded by ctx, for every handler still running: a stream's handler
// returns only after its connection row is closed, so when Shutdown returns
// nil no handler writes the store any more and the store may close. A wait
// cut by ctx logs hub.shutdown_wait_timeout and returns the error; the
// caller then calls Close as for http.Server.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.Server.Shutdown(ctx)
	n, werr := s.handlers.wait(ctx)
	if werr != nil {
		s.deps.log.WarnContext(ctx, "hub.shutdown_wait_timeout", "handlers", n)
		return errors.Join(err, fmt.Errorf("gateway: %d handlers still running at the end of the shutdown wait: %w", n, werr))
	}
	return err
}
