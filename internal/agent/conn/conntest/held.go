package conntest

import (
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Held lets one listening socket serve several http.Servers in turn. A test
// that restarts a fake hub "on the same port" must not close the socket and
// bind it again: between the two, another process on a busy machine can take
// the port (as a listener, or as the local end of an outgoing connection),
// and the bind fails with "address already in use". With Held the socket
// never closes during a swap, so the port is never free and connections that
// arrive meanwhile wait in the accept queue for the next server.
type Held struct {
	ln *net.TCPListener

	mu   sync.Mutex
	keep bool // views being closed leave the socket open
}

// Hold takes ln, which must be a TCP listener from [BindRetry] or
// net.ListenConfig.
func Hold(t *testing.T, ln net.Listener) *Held {
	t.Helper()
	tcp, ok := ln.(*net.TCPListener)
	if !ok {
		t.Fatalf("Hold: %T is not a TCP listener", ln)
	}
	return &Held{ln: tcp}
}

// Addr is the socket's address.
func (h *Held) Addr() net.Addr { return h.ln.Addr() }

// Close closes the socket.
func (h *Held) Close() { _ = h.ln.Close() }

// KeepOpen sets whether closing a view (http.Server.Close or Shutdown does)
// leaves the socket open. Turn it on, close the server, wait for its Serve
// to return, turn it off, then take a new [Held.View].
func (h *Held) KeepOpen(keep bool) {
	h.mu.Lock()
	h.keep = keep
	h.mu.Unlock()
}

// View is the listener one http.Server serves on. Closing it ends its
// Accept; the socket stays open only when [Held.KeepOpen] said so. Take the
// next view only after the last one's Serve has returned: no Accept is then
// in flight, so the deadline its Close set can be cleared.
func (h *Held) View() net.Listener {
	_ = h.ln.SetDeadline(time.Time{})
	return &view{h: h, closed: make(chan struct{})}
}

type view struct {
	h      *Held
	closed chan struct{}
	once   sync.Once
}

func (v *view) isClosed() bool {
	select {
	case <-v.closed:
		return true
	default:
		return false
	}
}

func (v *view) Accept() (net.Conn, error) {
	for {
		c, err := v.h.ln.Accept()
		if v.isClosed() {
			if err == nil {
				// Accepted as the view closed: dropped, as a stopping hub
				// drops it; the agent dials again.
				_ = c.Close()
			}
			return nil, net.ErrClosed
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		return c, err
	}
}

func (v *view) Close() error {
	var err error
	v.once.Do(func() {
		close(v.closed)
		v.h.mu.Lock()
		keep := v.h.keep
		v.h.mu.Unlock()
		if keep {
			// Wake the Accept in flight; the socket stays open.
			err = v.h.ln.SetDeadline(time.Now())
			return
		}
		v.h.Close()
	})
	return err
}

func (v *view) Addr() net.Addr { return v.h.ln.Addr() }

// BindRetry listens on addr, retrying while the address is in use (a port
// another process holds for a moment). It is for a test that must see the
// hub down first, so the socket cannot be held. After limit the test is
// skipped, not failed, because the port belongs to another process and not
// to the code under test; when the CI environment variable is set it fails
// instead, so a skip cannot hide a real problem in a gate run.
func BindRetry(t *testing.T, addr string, limit time.Duration) net.Listener {
	t.Helper()
	var lc net.ListenConfig
	end := time.Now().Add(limit)
	for {
		ln, err := lc.Listen(context.Background(), "tcp", addr)
		if err == nil {
			return ln
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			t.Fatal(err)
		}
		if time.Now().After(end) {
			t.Logf("%s stayed in use for %v: %v (find the holder with lsof -nP -iTCP -sTCP:LISTEN)", addr, limit, err)
			if os.Getenv("CI") != "" {
				t.Fatalf("%s stayed in use by another process for %v", addr, limit)
			}
			t.Skipf("skipped: %s stayed in use by another process for %v", addr, limit)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
