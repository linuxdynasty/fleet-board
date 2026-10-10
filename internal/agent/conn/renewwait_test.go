package conn_test

import (
	"errors"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

func TestTheRenewalCheckIsHourly(t *testing.T) {
	t.Parallel()
	if conn.DefaultRenewCheck != time.Hour {
		t.Errorf("DefaultRenewCheck = %v, want 1h (defaults.md)", conn.DefaultRenewCheck)
	}
}

// A stop during a renewal waits for the renewal to finish before Run
// returns: the renewal's closing line, renew.cancelled, is already logged
// when Run returns, even when the renewal takes a while to wind down.
func TestRunWaitsForACancelledRenewal(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	h.set(func(h *connectHub) { h.renew.hold = newGate() })
	a := enrolled(t, h)
	dueHub(h)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		conn.SetRenewHook(o, func(step string) bool {
			if step == "cancelled" {
				time.Sleep(300 * time.Millisecond)
				return true // a hook may delay the closing line, never drop it
			}
			return false
		})
	})
	waitFor(t, 10*time.Second, "the RenewCert", func() bool { return len(h.renewKeys()) == 1 })
	r.cancel()
	if err := r.result(t, 10*time.Second); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := a.events(t, "renew.cancelled"); len(got) != 1 {
		t.Fatalf("renew.cancelled lines when Run returned: %v, want 1", got)
	}
}

// A stream that drops during a renewal cancels it (renew.cancelled closes
// its renew.started), and the session waits for it, so no RenewCert is
// ever sent on a connection other than a stream's own; the next stream
// tries again with the same key.
func TestAStreamDroppedMidRenewalCancelsIt(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	hold := newGate()
	h.set(func(h *connectHub) { h.renew.hold = hold })
	a := enrolled(t, h)
	dueHub(h)
	startLoop(t, a, nil)
	waitFor(t, 10*time.Second, "the RenewCert", func() bool { return len(h.renewKeys()) == 1 })
	h.dropLive(t)
	waitFor(t, 10*time.Second, "the renewal on stream 2", func() bool { return len(h.renewKeys()) == 2 })
	if got := a.events(t, "renew.cancelled"); len(got) != 1 {
		t.Errorf("renew.cancelled = %v, want 1 for the dropped stream", got)
	}
	hold.open()
	waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	var streams []string
	for _, c := range h.calls.of("Connect") {
		streams = append(streams, c.remote)
	}
	for _, c := range h.calls.of("RenewCert") {
		if !slices.Contains(streams, c.remote) {
			t.Errorf("a RenewCert came from %s, a connection with no stream (%v)", c.remote, streams)
		}
	}
	if keys := h.renewKeys(); !keys[1].Equal(keys[0]) {
		t.Error("the next stream sent another key")
	}
}

// When renewal keeps failing, the stream stays up past the end of the
// certificate by hub time (the hub checks it only at the handshake), and
// the loop ends with EndCertExpired at the next attempt, without dialing.
func TestAFailingRenewalEndsAtTheNextAttemptAfterExpiry(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	h.set(func(h *connectHub) {
		h.renew.fail = func(int) error { return connect.NewError(connect.CodeUnavailable, errNoRenew) }
	})
	a := enrolled(t, h)
	hub := &skewClock{}
	var checks *atomic.Int64
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		conn.SetElapsed(o, hub.since)
		checks = countChecks(o)
	})
	h.connected(t, 1)
	hub.set(time.Until(enrolledLeaf(t, a).NotAfter) + time.Minute)
	waitChecks(t, checks, 5)
	r.running(t)
	h.dropLive(t)
	err := r.result(t, 10*time.Second)
	if ended, ok := errors.AsType[*conn.EndedError](err); !ok || ended.End != conn.EndCertExpired {
		t.Fatalf("Run = %v, want EndCertExpired", err)
	}
	if n := h.count(); n != 1 {
		t.Errorf("%d streams, want 1: the expired certificate is never dialed", n)
	}
	if exists(t, filepath.Join(a.dir, conn.EndedFile)) {
		t.Error("an expired certificate was recorded in ended.json")
	}
}

// The loop's expiry check runs on hub time too: an agent clock 40 days
// fast, past the end of the pair by its own reading, does not end the
// loop while the hub's time says the pair is good, and the expiry gauge
// reads the hub's time.
func TestAFastAgentClockDoesNotEndTheLoop(t *testing.T) {
	t.Parallel()
	h := startConnectHubWith(t, func(h *connectHub) {
		h.issueAt = func() time.Time { return time.Now().Add(-55 * 24 * time.Hour) }
	})
	a := enrolled(t, h)
	leaf := enrolledLeaf(t, a)
	agent := &skewClock{}
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = time.Hour
		conn.SetClock(o, agent.now)
	})
	h.connected(t, 1)
	agent.set(40 * 24 * time.Hour) // past the pair's end by the agent's clock
	h.dropLive(t)
	h.connected(t, 2)
	r.running(t)
	exp, err := strconv.ParseFloat(metric(scrape(t, a), "fleet_agent_cert_expiry_seconds"), 64)
	if want := time.Until(leaf.NotAfter).Seconds(); err != nil || want <= 0 || math.Abs(exp-want) > 60 {
		t.Errorf("fleet_agent_cert_expiry_seconds = %v (%v), want about %v by hub time", exp, err, want)
	}
}
