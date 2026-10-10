package conn_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// closedAddr is a loopback address with nothing listening.
func closedAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// closedAddrs returns n distinct loopback addresses nobody listens on. The
// listeners stay open until all n ports are picked: one closedAddr call per
// address can hand the same port out twice (the system reuses a port it just
// freed), and the loop tries a repeated address once.
func closedAddrs(t *testing.T, n int) []string {
	t.Helper()
	var lc net.ListenConfig
	var held []net.Listener
	addrs := make([]string, 0, n)
	for range n {
		ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, ln)
		addrs = append(addrs, ln.Addr().String())
	}
	for _, ln := range held {
		_ = ln.Close()
	}
	return addrs
}

func TestStatusKeepsTheLastFailurePerHubAddress(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	dead := closedAddr(t)
	imp := startImpostor(t, serverCert(t, newCA(t), []string{"127.0.0.1"}))
	if err := os.WriteFile(filepath.Join(a.dir, conn.HubAddressFile), []byte(dead+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Identity.HubAddresses = []string{imp.addr, h.addr}
		o.PushOff = func() bool { return true }
	})
	h.connected(t, 1)
	r.connected(t)
	waitFor(t, 5*time.Second, "the cached address to change", func() bool {
		cached, err := conn.LoadHubAddress(a.dir)
		return err == nil && cached == h.addr
	})

	st := r.loop.Status()
	// Neither got past the handshake, so both failed dialing.
	for _, addr := range []string{dead, imp.addr} {
		if f, ok := st.Failures[addr]; !ok || f.Step != "dial" || f.ErrKind != "refused" || f.Time.IsZero() {
			t.Errorf("the failure kept for %s = %+v, want step dial, err_kind refused", addr, f)
		}
	}
	if _, ok := st.Failures[h.addr]; ok || len(st.Failures) != 2 {
		t.Errorf("failures = %+v", st.Failures)
	}
	if !st.PushOff || st.Peer != h.addr || st.State != conn.StateConnected {
		t.Errorf("status = %+v", st)
	}
	if n := imp.received.Load(); n != 0 {
		t.Errorf("the impostor received %d bytes", n)
	}
	if got := a.events(t, "connect.failed"); len(got) != 2 || got[0]["step"] != "dial" || got[1]["step"] != "dial" {
		t.Errorf("connect.failed = %v, want two at step dial", got)
	}
}

func TestAnOversizeSnapshotIsNotSent(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.Snapshot = func(_ context.Context, s *agentv1.Snapshot) error {
			s.Facts.Os = strings.Repeat("x", 1<<20)
			return nil
		}
	})
	waitFor(t, 10*time.Second, "two tries", func() bool { return h.count() >= 2 })
	if n := len(h.snapshots(1)); n != 0 {
		t.Errorf("the hub got %d snapshots over the cap", n)
	}
	if st := r.loop.Status(); st.SnapshotOversize.IsZero() {
		t.Errorf("status = %+v, want the oversize time", st)
	}
	over := a.events(t, "snapshot.oversize")
	if len(over) == 0 || over[0]["max_bytes"] != float64(960<<10) {
		t.Errorf("snapshot.oversize = %v", over)
	}
	r.running(t)
}

func scrape(t *testing.T, a *agent) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	a.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

func TestTheLoopReportsItsMetrics(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	logResult(t, a.openDB(t), "c1", time.Now())
	startLoop(t, a, nil)
	h.connected(t, 1)
	h.dropLive(t)
	h.connected(t, 2)
	waitFor(t, 5*time.Second, "the second connect to count", func() bool {
		return metric(scrape(t, a), "fleet_agent_connect_attempts_total", `result="connected"`) == "2"
	})
	m := scrape(t, a)
	for _, c := range []struct {
		name   string
		labels []string
		value  string
	}{
		{"fleet_agent_connected", nil, "1"},
		{"fleet_agent_connect_attempts_total", []string{`result="connected"`, `source="cached"`}, "2"},
		{"fleet_agent_reconnects_total", []string{`reason="reconnect"`}, "1"},
		{"fleet_agent_unacked_results", nil, "1"},
	} {
		if got := metric(m, c.name, c.labels...); got != c.value {
			t.Errorf("%s%v = %q, want %s", c.name, c.labels, got, c.value)
		}
	}
	if v, err := strconv.ParseFloat(metric(m, "fleet_agent_cert_expiry_seconds"), 64); err != nil || v <= 0 {
		t.Errorf("fleet_agent_cert_expiry_seconds = %v, %v", v, err)
	}
	h.push(t, resultAck("c1"))
	waitFor(t, 5*time.Second, "the ack to count", func() bool {
		return metric(scrape(t, a), "fleet_agent_unacked_results") == "0"
	})
}

// metric is the value of the sample of name that carries every label.
func metric(text, name string, labels ...string) string {
	for line := range strings.Lines(text) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), name+"{")
		if !ok {
			continue
		}
		set, value, ok := strings.Cut(rest, "} ")
		if !ok {
			continue
		}
		all := true
		for _, l := range labels {
			all = all && strings.Contains(","+set+",", ","+l+",")
		}
		if all {
			return value
		}
	}
	return ""
}
