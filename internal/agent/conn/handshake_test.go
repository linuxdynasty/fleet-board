package conn_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// dropper accepts connections and drops each during the handshake.
type dropper struct {
	addr     string
	accepted atomic.Int64
	mu       sync.Mutex
	got      []byte // every byte read, all of it ClientHello
}

// startDropper closes each connection at once, or, with reset, reads the
// ClientHello record and then resets the connection.
func startDropper(t *testing.T, reset bool) *dropper {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	d := &dropper{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			d.accepted.Add(1)
			if tc, ok := c.(*net.TCPConn); ok && reset {
				d.readHello(c)
				_ = tc.SetLinger(0)
			}
			_ = c.Close()
		}
	}()
	return d
}

// readHello reads one TLS record: the 5-byte header, then its body.
func (d *dropper) readHello(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	body := make([]byte, int(hdr[3])<<8|int(hdr[4]))
	n, _ := io.ReadFull(c, body)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.got = append(append(d.got, hdr...), body[:n]...)
}

// 03 W1: a connection that drops or resets during the handshake is a lost
// connection, retried within the enroll tries, never an untrusted hub.
func TestAHandshakeCutShortIsRetried(t *testing.T) {
	t.Parallel()
	for _, reset := range []bool{false, true} {
		name := map[bool]string{false: "closed at once", true: "reset after the ClientHello"}[reset]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			d := startDropper(t, reset)
			tok, err := ca.NewToken(newCA(t).Cert.Raw, rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			a := newAgent(t)
			_, err = a.enroll(t, d.addr, tok.Reveal())
			if !errors.Is(err, conn.ErrHubFailed) || errors.Is(err, conn.ErrHubUntrusted) {
				t.Fatalf("Enroll = %v, want ErrHubFailed, not ErrHubUntrusted", err)
			}
			if n := d.accepted.Load(); n != conn.DefaultAttempts {
				t.Errorf("%d connections, want one per try (%d)", n, conn.DefaultAttempts)
			}
			if got := len(a.events(t, "enroll.retry")); got != conn.DefaultAttempts-1 {
				t.Errorf("%d enroll.retry lines, want %d", got, conn.DefaultAttempts-1)
			}
			if lines := a.events(t, "enroll.hub_untrusted"); len(lines) != 0 {
				t.Errorf("enroll.hub_untrusted lines %v; a lost connection is not an untrusted hub", lines)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			switch {
			case !reset && len(d.got) != 0:
				t.Errorf("the listener read %d bytes, want 0", len(d.got))
			case reset && (len(d.got) == 0 || d.got[0] != 0x16):
				t.Errorf("the listener read %d bytes, want only ClientHello records", len(d.got))
			case bytes.Contains(d.got, []byte(tok.Reveal()[len("fbe1_"):])):
				t.Error("the token reached the listener")
			}
		})
	}
}
