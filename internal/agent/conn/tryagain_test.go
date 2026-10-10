package conn_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// silentAddr accepts TCP connections and never answers, like an address
// with no route that drops packets instead of refusing them.
func silentAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan net.Conn, 64)
	t.Cleanup(func() {
		_ = ln.Close()
		close(held)
		for c := range held {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case held <- c:
			default:
				_ = c.Close()
			}
		}
	}()
	return ln.Addr().String()
}

// onlyAddress makes addr the agent's one hub address, cached and configured.
func onlyAddress(t *testing.T, a *agent, addr string) func(*conn.LoopOptions) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.dir, conn.HubAddressFile), []byte(addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(o *conn.LoopOptions) { o.Identity.HubAddresses = []string{addr} }
}

// The loop never gives up on a hub that keeps failing the same way: it
// backs off and tries again, every time.
func TestTheSameFailureIsTriedAgain(t *testing.T) {
	t.Parallel()
	unavailable := func(int) error { return connect.NewError(connect.CodeUnavailable, errors.New("busy")) }
	cases := []struct {
		name string
		// addr sets up the hub and returns the only address to try.
		addr func(t *testing.T, h *connectHub) string
		// steps are the steps a failure may be logged at; kind is its
		// err_kind, or any when empty.
		steps []string
		kind  string
		// hubSees is true when each try reaches the hub's Connect.
		hubSees bool
		// result is the connect_attempts_total result each try counts as,
		// or any when empty.
		result string
		// handshake, when set, is the TLS handshake timeout; only the
		// address that never answers needs a short one, and a short one
		// elsewhere could time out a real handshake under load.
		handshake time.Duration
	}{
		{
			name: "a closed port", addr: func(t *testing.T, _ *connectHub) string { t.Helper(); return closedAddr(t) },
			steps: []string{"dial"}, kind: "refused", result: "failed",
		},
		{name: "an impostor", addr: func(t *testing.T, _ *connectHub) string {
			t.Helper()
			return startImpostor(t, serverCert(t, newCA(t), []string{"127.0.0.1"})).addr
		}, steps: []string{"dial"}, kind: "refused", result: "untrusted"},
		{name: "an address that never answers", addr: func(t *testing.T, _ *connectHub) string {
			t.Helper()
			return silentAddr(t)
		}, steps: []string{"dial"}, kind: "timeout", result: "failed", handshake: 50 * time.Millisecond},
		{name: "a hub answering unavailable", addr: func(_ *testing.T, h *connectHub) string {
			h.set(func(h *connectHub) { h.refuse = unavailable })
			return h.addr
		}, steps: []string{"hello"}, hubSees: true, result: "failed"},
		{name: "a reset right after HelloAck", addr: func(_ *testing.T, h *connectHub) string {
			h.set(func(h *connectHub) { h.cutAfterAck = unavailable })
			return h.addr
		}, steps: []string{"hello", "snapshot", "stream"}, hubSees: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const backoff = 60 * time.Millisecond
			h := startConnectHub(t)
			a := enrolled(t, h)
			addr := tc.addr(t, h)
			only := onlyAddress(t, a, addr)
			r := startLoop(t, a, func(o *conn.LoopOptions) {
				only(o)
				o.HandshakeTimeout = tc.handshake
				o.Backoff.First, o.Backoff.Max = backoff, backoff
			})
			failures := func() []map[string]any {
				var out []map[string]any
				for _, e := range append(a.events(t, "connect.failed"), a.events(t, "connect.closed")...) {
					if e["peer"] == addr {
						out = append(out, e)
					}
				}
				return out
			}
			waitFor(t, 20*time.Second, "three failed tries", func() bool { return len(failures()) >= 3 })
			r.running(t)
			for _, f := range failures() {
				step, _ := f["step"].(string)
				if !slices.Contains(tc.steps, step) || (tc.kind != "" && f["err_kind"] != tc.kind) {
					t.Errorf("a try failed as %v, want step in %v and err_kind %q", f, tc.steps, tc.kind)
				}
			}
			if tc.hubSees && h.count() < 3 {
				t.Errorf("the hub saw %d tries, want at least 3", h.count())
			}
			// Each try waits out the backoff after the one before.
			attempts := a.events(t, "connect.attempt")
			if len(attempts) < 3 {
				t.Errorf("%d connect.attempt lines, want at least 3", len(attempts))
			}
			const logSkew = 10 * time.Millisecond
			for i := 1; i < len(attempts); i++ {
				if d := logTime(t, attempts[i]).Sub(logTime(t, attempts[i-1])); d < backoff-logSkew {
					t.Errorf("tries %d and %d are %v apart, want at least the %v backoff", i, i+1, d, backoff)
				}
			}
			if f, ok := r.loop.Status().Failures[addr]; !ok || !slices.Contains(tc.steps, f.Step) ||
				(tc.kind != "" && f.ErrKind != tc.kind) {
				t.Errorf("the status keeps %+v for %s, want a step in %v and err_kind %q", f, addr, tc.steps, tc.kind)
			}
			if tc.result != "" {
				m := scrape(t, a)
				if n, _ := strconv.Atoi(metric(m, "fleet_agent_connect_attempts_total",
					`result="`+tc.result+`"`, `source="cached"`)); n < 3 {
					t.Errorf(`connect_attempts_total{result=%q} = %d, want at least 3`, tc.result, n)
				}
				if got := metric(m, "fleet_agent_connected"); got != "0" {
					t.Errorf("fleet_agent_connected = %q, want 0", got)
				}
			}
			r.cancel()
			if err := r.result(t, 10*time.Second); err != nil {
				t.Errorf("Run after cancel = %v, want nil", err)
			}
		})
	}
}

// After Bye{HUB_RESTART} on a connected stream and the hub coming back on
// the same port, the agent reconnects and reports the epoch it had.
func TestAHubRestartIsAReconnect(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	r.connected(t)
	h.restart(t)
	h.connected(t, 2)
	hello := h.stream(2).hello
	if hello.GetReason() != agentv1.ConnectReason_CONNECT_REASON_RECONNECT || hello.GetLastEpoch() != 1 {
		t.Errorf("the Hello after a hub restart = reason %v last_epoch %d; want RECONNECT and 1 (the epoch before)",
			hello.GetReason(), hello.GetLastEpoch())
	}
	if byes := a.events(t, "connect.bye"); len(byes) != 1 || byes[0]["reason"] != "HUB_RESTART" {
		t.Errorf("connect.bye = %v", byes)
	}
	r.running(t)
}

// A stream that reached Connected resets the backoff: after it closes, the
// next try waits First again, not the doubled wait the failures before it
// had built up (defaults.md "Reconnect backoff").
func TestAConnectResetsTheBackoff(t *testing.T) {
	t.Parallel()
	const first = 20 * time.Millisecond
	const refused = 5 // without the reset, the waits after would be wait(6) and up: 20 ms to 1.28 s
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(func(h *connectHub) {
		h.refuse = func(n int) error {
			if n <= refused {
				return connect.NewError(connect.CodeUnavailable, errors.New("busy"))
			}
			return nil
		}
	})
	r := startLoop(t, a, func(o *conn.LoopOptions) { o.Backoff.First, o.Backoff.Max = first, 5*time.Second })
	for n := refused + 1; n <= refused+3; n++ {
		h.connected(t, n)
		h.push(t, &agentv1.ConnectResponse{Body: &agentv1.ConnectResponse_Bye{
			Bye: bye(agentv1.ByeReason_BYE_REASON_HUB_RESTART),
		}})
	}
	h.connected(t, refused+4)
	byes, attempts := a.events(t, "connect.bye"), a.events(t, "connect.attempt")
	if len(byes) != 3 || len(attempts) < refused+4 {
		t.Fatalf("%d connect.bye and %d connect.attempt lines, want 3 and %d", len(byes), len(attempts), refused+4)
	}
	// wait(1) is First to 2*First; the slack covers that and the reconnect.
	const slack = 150 * time.Millisecond
	for i, b := range byes {
		next := attempts[refused+1+i]
		if d := logTime(t, next).Sub(logTime(t, b)); d < first-10*time.Millisecond || d > first+slack {
			t.Errorf("after connected stream %d closed, the next try came %v later, want about %v",
				refused+1+i, d, first)
		}
	}
	r.running(t)
}
