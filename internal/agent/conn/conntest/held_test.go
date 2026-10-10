package conntest_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// acceptProbe reports when its Accept is called for the first time, so a
// test knows Serve is blocked in Accept.
type acceptProbe struct {
	net.Listener
	accepting chan struct{}
}

func (p *acceptProbe) Accept() (net.Conn, error) {
	select {
	case <-p.accepting:
	default:
		close(p.accepting)
	}
	return p.Listener.Accept()
}

// Closing a server whose socket is kept open wakes its Accept at once, with
// no client traffic, and the socket then serves the next server.
func TestAKeptSocketWakesTheOldServerAndServesTheNext(t *testing.T) {
	t.Parallel()
	held := conntest.Hold(t, conntest.BindRetry(t, "127.0.0.1:0", time.Second))
	t.Cleanup(held.Close)
	addr := held.Addr().String()

	first := &http.Server{ReadHeaderTimeout: time.Second}
	probe := &acceptProbe{Listener: held.View(), accepting: make(chan struct{})}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_ = first.Serve(probe)
	}()
	select {
	case <-probe.accepting:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve never called Accept")
	}
	// Serve has called Accept, but may not have entered the accept call
	// yet; Close is safe either way, and the next Accept sees the deadline.
	held.KeepOpen(true)
	closed := make(chan struct{})
	go func() { defer close(closed); _ = first.Close() }()
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the old server's Serve did not return within 2s of its Close, with the socket kept open")
	}
	<-closed
	held.KeepOpen(false)

	second := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
	}
	secondDone := make(chan struct{})
	ln := held.View()
	go func() {
		defer close(secondDone)
		_ = second.Serve(ln)
	}()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr, http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET after the swap: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204 from the second server", res.StatusCode)
	}
	_ = second.Close()
	<-secondDone
}

// A thief stops when the test that started it ends, also when that test
// fails midway (Cleanup runs after a Fatal as well).
func TestAThiefStopsWithItsTest(t *testing.T) {
	t.Parallel()
	var thief *conntest.Thief
	t.Run("scope", func(t *testing.T) {
		thief = conntest.StartThief(t, "127.0.0.1:1")
		select {
		case <-thief.Done():
			t.Fatal("the thief stopped before its test ended")
		default:
		}
	})
	select {
	case <-thief.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the thief still runs after its test ended")
	}
}
