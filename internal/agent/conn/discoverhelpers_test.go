package conn_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// fakeDiscover stands in for mDNS: each Browse hands over its services in
// order, then, with hold, waits out the timeout or the cancel like the
// library does.
type fakeDiscover struct {
	mu       sync.Mutex
	services []conn.Service
	err      error
	hold     bool
	// onBrowse runs at the start of each Browse.
	onBrowse func()
	calls    int
}

func (f *fakeDiscover) Browse(ctx context.Context, timeout time.Duration, found func(conn.Service)) error {
	f.mu.Lock()
	f.calls++
	svcs, err, hold, hook := slices.Clone(f.services), f.err, f.hold, f.onBrowse
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if err != nil {
		return err
	}
	for _, s := range svcs {
		found(s)
	}
	if hold {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
	return nil
}

func (f *fakeDiscover) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

var lo4 = netip.MustParseAddr("127.0.0.1")

// service is an mDNS answer for a fleetd at 127.0.0.1:port named host.
func service(t *testing.T, addr, host string) conn.Service {
	t.Helper()
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return conn.Service{Name: "fleetd-" + host + "._fleetd._tcp.local.", Host: host + ".", Port: port, AddrV4: lo4}
}

// nameHub issues h a server certificate that also names names, and
// serves h again on the same port, as an mDNS-advertised hub would be
// named.
func nameHub(t *testing.T, h *connectHub, names ...string) {
	t.Helper()
	h.mu.Lock()
	srv := h.srv
	h.tlsCfg.Certificates = []tls.Certificate{
		serverCert(t, h.auth, append([]string{"127.0.0.1"}, names...), h.auth.Cert.Raw),
	}
	h.mu.Unlock()
	if err := srv.Close(); err != nil {
		t.Fatal(err)
	}
	// A Close that comes before Serve took the listener leaves Serve to
	// close it; wait until the port is free.
	waitFor(t, 5*time.Second, "the hub's port to be free", func() bool {
		var lc net.ListenConfig
		ln, err := lc.Listen(context.Background(), "tcp", h.addr)
		if err != nil {
			return false
		}
		_ = ln.Close()
		return true
	})
	h.listen(t, h.addr)
}

// mdnsAddr is the hub address an mDNS answer for host gives h.
func mdnsAddr(t *testing.T, h *connectHub, host string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(h.addr)
	if err != nil {
		t.Fatal(err)
	}
	return net.JoinHostPort(host, p)
}

// writeState writes a file of the agent state folder the way the agent
// does (mode 0600).
func writeState(t *testing.T, a *agent, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.dir, name), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func removeState(t *testing.T, a *agent, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(a.dir, name)); err != nil {
		t.Fatal(err)
	}
}

// attempts are the connect.attempt lines as source and peer.
func attempts(t *testing.T, a *agent) [][2]string {
	t.Helper()
	var out [][2]string
	for _, l := range a.events(t, "connect.attempt") {
		src, _ := l["source"].(string)
		peer, _ := l["peer"].(string)
		out = append(out, [2]string{src, peer})
	}
	return out
}

// useMDNS has the loop browse with d, with the default budgets and the
// test hubs' loopback answers let through.
func useMDNS(o *conn.LoopOptions, d conn.Discover) {
	o.Discover = d
	conn.SetMDNSLimits(o, 0, 0)
}

// enrollMDNS is useMDNS for Enroll.
func enrollMDNS(d conn.Discover) func(*conn.Options) {
	return func(o *conn.Options) {
		o.Discover = d
		conn.SetEnrollMDNSLimits(o, 0, 0)
	}
}

// blackhole is a TCP listener that accepts and never answers, until the
// test ends.
func blackhole(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var held []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	return ln.Addr().String()
}
