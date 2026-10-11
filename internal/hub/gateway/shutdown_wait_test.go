package gateway_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// heldRowClose is a hub whose Connect handlers wait before their row
// close until free is called, with one machine connected on a transport of
// its own.
type heldRowClose struct {
	h       *hub
	m       machine
	s       *agentStream
	tr      *http.Transport
	entered chan struct{}
	free    func()
}

func startHeldRowClose(t *testing.T) *heldRowClose {
	t.Helper()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	h := startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetBeforeRowClose(o, func() {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
		})
	})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	t.Cleanup(free)
	m := h.enrollMachine(t)
	s, tr := h.openOwn(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	return &heldRowClose{h: h, m: m, s: s, tr: tr, entered: entered, free: free}
}

// shutdown starts Shutdown with a wait of d, takes the Bye{HUB_RESTART},
// then leaves as the real agent does (it resets its side of the stream and
// drops the connection) and waits until the handler holds its row close.
func (x *heldRowClose) shutdown(t *testing.T, d time.Duration) <-chan error {
	t.Helper()
	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		defer cancel()
		stopped <- x.h.srv.Shutdown(ctx)
	}()
	if got := x.s.next(t).GetBye().GetReason(); got != agentv1.ByeReason_BYE_REASON_HUB_RESTART {
		t.Fatalf("message Bye{%v}, want Bye{HUB_RESTART}", got)
	}
	x.s.cancel()
	_ = x.s.bs.CloseRequest()
	_ = x.s.bs.CloseResponse()
	x.tr.CloseIdleConnections()
	select {
	case <-x.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never reached its row close")
	}
	return stopped
}

// Shutdown returns only after every Connect handler has closed its row:
// an agent that drops the connection on the Bye must not let Shutdown
// return, and the store close after it, while the row close is still to
// run. Before the fix the row stayed live and the next Hello closed it as
// displaced, not hub_restart.
func TestShutdownWaitsForTheRowCloseOfAStreamTheAgentDropped(t *testing.T) {
	t.Parallel()
	x := startHeldRowClose(t)
	stopped := x.shutdown(t, 10*time.Second)
	select {
	case err := <-stopped:
		t.Fatalf("Shutdown returned (%v) while the row close was still to run", err)
	case <-time.After(time.Second):
	}
	x.free()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return after the row close")
	}
	if got := x.h.conns(t, x.m.id); len(got) != 1 || got[0].endReason != "hub_restart" {
		t.Errorf("rows %+v when Shutdown returned, want one closed as hub_restart", got)
	}
}

// A row close still stuck when Shutdown's context ends: Shutdown returns
// the context's error (the caller then closes the server) and logs
// hub.shutdown_wait_timeout with the count of handlers still running.
func TestShutdownWaitEndsWithItsContext(t *testing.T) {
	t.Parallel()
	x := startHeldRowClose(t)
	stopped := x.shutdown(t, 500*time.Millisecond)
	select {
	case err := <-stopped:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown: %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not return at the end of its context")
	}
	lines := x.h.events(t, "hub.shutdown_wait_timeout")
	if len(lines) != 1 || lines[0]["handlers"] != float64(1) {
		t.Errorf("hub.shutdown_wait_timeout lines %v, want one with handlers 1", lines)
	}
}

// openOwn is open on a transport of its own, returned so the test can drop
// the connection the stream runs on.
func (h *hub) openOwn(t *testing.T, m machine) (*agentStream, *http.Transport) {
	t.Helper()
	cfg := clientTLS(h.fixture, "localhost")
	cfg.Certificates = []tls.Certificate{m.cert}
	tr := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}
	t.Cleanup(tr.CloseIdleConnections)
	c := agentv1connect.NewAgentServiceClient(&http.Client{Transport: tr}, "https://"+h.addr, connect.WithGRPC())
	ctx, cancel := context.WithCancel(context.Background())
	s := &agentStream{
		bs: c.Connect(ctx), cancel: cancel,
		msgs: make(chan *agentv1.ConnectResponse, 16), end: make(chan error, 1),
	}
	t.Cleanup(func() {
		cancel()
		_ = s.bs.CloseRequest()
		_ = s.bs.CloseResponse()
	})
	go func() {
		for {
			r, err := s.bs.Receive()
			if err != nil {
				s.end <- err
				return
			}
			s.msgs <- r
		}
	}()
	return s, tr
}
