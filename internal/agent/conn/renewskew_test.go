package conn_test

import (
	"math"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// A renewed pair that starts in the future (a hub that signed with a clock
// two hours ahead) is not presented until it starts: the agent dials with
// the enrolled pair, which is still good, and the hub's handshake (on the
// shared clock) takes it. The renewal decision follows the pair that ends
// last, so the enrolled pair, past two thirds, starts no renewal. Once the
// clock passes the renewed pair's start, the next handshake presents it.
func TestARenewedPairIsNotPresentedBeforeItStarts(t *testing.T) {
	t.Parallel()
	clock := &skewClock{}
	h := startConnectHubWith(t, func(h *connectHub) {
		h.tlsCfg.Time = clock.now
		h.issueAt = func() time.Time { return time.Now().Add(overdue) }
	})
	a := enrolled(t, h)
	enrolledSerial := serialOf(enrolledLeaf(t, a))
	data, renewed, _ := pairFile(t, h.auth, fakeMachine, time.Now().Add(2*time.Hour))
	writeFile(t, filepath.Join(a.dir, conn.CertFile), data, 0o600)
	var checks *atomic.Int64
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		conn.SetClock(o, clock.now)
		checks = countChecks(o)
	})
	h.connected(t, 1)
	waitChecks(t, checks, 10)
	if got := h.calls.of("Connect")[0].serial; got != enrolledSerial {
		t.Fatalf("the agent presented %s, want the enrolled %s while the renewed one has not started", got, enrolledSerial)
	}
	if n := len(h.renewKeys()); n != 0 {
		t.Fatalf("%d RenewCert calls; the pair that ends last is not due", n)
	}
	if got := a.events(t, "renew.pair_loaded"); len(got) != 1 || got[0]["source"] != "cert_file" || got[0]["dial_source"] != "identity" {
		t.Errorf("renew.pair_loaded = %v, want source=cert_file dial_source=identity", got)
	}
	// The expiry gauge follows the pair that ends last, started or not.
	exp, err := strconv.ParseFloat(metric(scrape(t, a), "fleet_agent_cert_expiry_seconds"), 64)
	if want := time.Until(renewed.NotAfter).Seconds(); err != nil || math.Abs(exp-want) > 60 {
		t.Errorf("fleet_agent_cert_expiry_seconds = %v (%v), want about %v (the renewed pair)", exp, err, want)
	}
	clock.set(time.Until(renewed.NotBefore) + time.Minute)
	h.dropLive(t)
	h.connected(t, 2)
	if got := h.calls.of("Connect")[1].serial; got != serialOf(renewed) {
		t.Errorf("after the renewed pair started the agent presented %s, want %s", got, serialOf(renewed))
	}
}

// The same, when the future pair arrives live: a renewal answer whose
// certificate starts two hours ahead of the agent's clock is saved, the
// enrolled pair stays a candidate, the next handshake presents it, no
// second renewal starts, and once the clock passes the new pair's start
// the next handshake presents the new pair.
func TestALiveRenewalOfAFuturePairKeepsTheEnrolledPair(t *testing.T) {
	t.Parallel()
	clock := &skewClock{}
	h := startConnectHubWith(t, func(h *connectHub) {
		h.tlsCfg.Time = clock.now
		h.hubClock = func() time.Time { return time.Now().Add(twoThirds) }
		h.renew.issueAt = func() time.Time { return time.Now().Add(2 * time.Hour) }
	})
	a := enrolled(t, h)
	enrolledSerial := serialOf(enrolledLeaf(t, a))
	var checks *atomic.Int64
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		conn.SetClock(o, clock.now)
		checks = countChecks(o)
	})
	waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	renewed := savedPair(t, a)
	if !renewed.NotBefore.After(time.Now()) {
		t.Fatalf("the renewed pair starts %v, not in the agent's future", renewed.NotBefore)
	}
	waitChecks(t, checks, 10)
	if n := len(h.renewKeys()); n != 1 {
		t.Errorf("%d RenewCert calls, want 1", n)
	}
	h.dropLive(t)
	h.connected(t, 2)
	if got := h.calls.of("Connect")[1].serial; got != enrolledSerial {
		t.Errorf("after a live renewal of a future pair the agent presented %s, want the enrolled %s", got, enrolledSerial)
	}
	clock.set(time.Until(renewed.NotBefore) + time.Minute)
	h.dropLive(t)
	h.connected(t, 3)
	if got := h.calls.of("Connect")[2].serial; got != serialOf(renewed) {
		t.Errorf("after the new pair started the agent presented %s, want %s", got, serialOf(renewed))
	}
}
