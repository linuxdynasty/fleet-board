package conn_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// proxy forwards TCP to a target; freeze makes the connections open so far
// go silent both ways while they stay open, like a hub that hangs or a
// network path that died without a reset.
type proxy struct {
	addr string

	mu     sync.Mutex
	frozen []*atomic.Bool
}

func startProxy(t *testing.T, target string) *proxy {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{addr: ln.Addr().String()}
	var conns sync.WaitGroup
	var open []net.Conn
	var openMu sync.Mutex
	t.Cleanup(func() {
		_ = ln.Close()
		openMu.Lock()
		for _, c := range open {
			_ = c.Close()
		}
		openMu.Unlock()
		conns.Wait()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var d net.Dialer
			up, err := d.DialContext(context.Background(), "tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			openMu.Lock()
			open = append(open, c, up)
			openMu.Unlock()
			silent := &atomic.Bool{}
			p.mu.Lock()
			p.frozen = append(p.frozen, silent)
			p.mu.Unlock()
			conns.Add(2)
			go pipe(&conns, up, c, silent)
			go pipe(&conns, c, up, silent)
		}
	}()
	return p
}

// pipe copies src to dst, dropping every byte once silent is set.
func pipe(wg *sync.WaitGroup, dst, src net.Conn, silent *atomic.Bool) {
	defer wg.Done()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 && !silent.Load() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				_ = dst.Close()
			}
			return
		}
	}
}

func (p *proxy) freeze() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.frozen {
		f.Store(true)
	}
}

// keepaliveRun connects through a proxy, silences it and returns how long
// the agent took to give up on the stream and connect again.
func keepaliveRun(t *testing.T, ka conn.Keepalive, limit time.Duration) time.Duration {
	t.Helper()
	h := startConnectHub(t)
	a := enrolled(t, h)
	p := startProxy(t, h.addr)
	if err := os.WriteFile(filepath.Join(a.dir, conn.HubAddressFile), []byte(p.addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	startLoop(t, a, func(o *conn.LoopOptions) { o.Keepalive = ka })
	h.connected(t, 1)
	p.freeze()
	silenced := time.Now()
	waitFor(t, limit, "a new stream after the hub went silent", func() bool {
		return h.count() >= 2 && len(h.snapshots(2)) > 0
	})
	closed := a.events(t, "connect.closed")
	if len(closed) != 1 || closed[0]["step"] != "stream" {
		t.Fatalf("connect.closed = %v", closed)
	}
	// Measured to the agent's own connect.closed line, not to the new
	// stream, so the reconnect does not count.
	return logTime(t, closed[0]).Sub(silenced)
}

func TestKeepaliveCatchesASilentHub(t *testing.T) {
	t.Parallel()
	ka := conn.Keepalive{Ping: 100 * time.Millisecond, Timeout: 300 * time.Millisecond}
	// Detection takes up to Ping + Timeout (400 ms), plus scheduling. The bound
	// is twice that: a loaded machine can add well over 150 ms, and a timeout
	// three times too long (about 1 s) or the default (30 s) still fails.
	limit := (ka.Ping + ka.Timeout) * 2
	if took := keepaliveRun(t, ka, 5*time.Second); took > limit {
		t.Errorf("the silent hub was caught after %v, want within %v", took, limit)
	}
}

// TestKeepaliveDefaultsCatchASilentHubWithin30s runs the default 10 s ping
// and 20 s timeout in real time; set FLEET_SLOW_TESTS=1 to run it.
func TestKeepaliveDefaultsCatchASilentHubWithin30s(t *testing.T) {
	if os.Getenv("FLEET_SLOW_TESTS") == "" {
		t.Skip("real-time keepalive; set FLEET_SLOW_TESTS=1")
	}
	t.Parallel()
	took := keepaliveRun(t, conn.Keepalive{}, 45*time.Second)
	t.Logf("the default keepalive caught the silent hub after %v", took)
	if took > 30*time.Second+500*time.Millisecond {
		t.Errorf("caught after %v, want within 30 s", took)
	}
}
