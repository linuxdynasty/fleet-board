package conn_test

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// Only one renewal runs at a time: while the hub holds the first call,
// later checks send nothing.
func TestOneRenewalRunsAtATime(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	hold := newGate()
	h.set(func(h *connectHub) { h.renew.hold = hold })
	a := enrolled(t, h)
	dueHub(h)
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 5 * time.Millisecond
	})
	waitFor(t, 10*time.Second, "the first RenewCert", func() bool { return len(h.renewKeys()) == 1 })
	time.Sleep(100 * time.Millisecond) // twenty checks while it is held
	if n := len(h.renewKeys()); n != 1 {
		t.Fatalf("%d RenewCert calls while one was held, want 1", n)
	}
	hold.open()
	waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	if n := len(h.renewKeys()); n != 1 {
		t.Errorf("%d RenewCert calls, want 1", n)
	}
}

// A stop during a renewal ends Run at once, saves nothing, logs no
// failure and keeps renew.key, so the next run sends the same key.
func TestAStopDuringARenewalKeepsItsKey(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	h.set(func(h *connectHub) { h.renew.hold = newGate() })
	a := enrolled(t, h)
	dueHub(h)
	r := startLoop(t, a, nil)
	waitFor(t, 10*time.Second, "the RenewCert", func() bool { return len(h.renewKeys()) == 1 })
	start := time.Now()
	r.cancel()
	if err := r.result(t, 10*time.Second); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Run took %v to stop during a renewal", d)
	}
	if got := a.events(t, "renew.failed"); len(got) != 0 {
		t.Errorf("a stop was logged as a failure: %v", got)
	}
	if exists(t, filepath.Join(a.dir, conn.CertFile)) || !exists(t, filepath.Join(a.dir, conn.RenewKeyFile)) {
		t.Fatalf("after a stop: %s saved or %s gone", conn.CertFile, conn.RenewKeyFile)
	}
	h.set(func(h *connectHub) { h.renew.hold = nil })
	dueHub(h)
	startLoop(t, a, nil)
	waitFor(t, 10*time.Second, "the renewal on the next run", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	if keys := h.renewKeys(); len(keys) != 2 || !keys[1].Equal(keys[0]) {
		t.Errorf("the next run sent another key (%d calls)", len(keys))
	}
}

// When cert.pem cannot be written, the renewal is logged as failed at
// step save, renew.key is kept, the loop keeps going, and the next check
// tries again with the same key until the save works. The hub answers each
// try (a fake that signs again; the real hub returns its stored one).
func TestAnUnwritableCertFileIsRetried(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	// A folder in cert.pem's place: the rename over it fails.
	blocker := filepath.Join(a.dir, conn.CertFile)
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	dueHub(h)
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
	})
	waitFor(t, 10*time.Second, "two failed saves", func() bool {
		n := 0
		for _, e := range a.events(t, "renew.failed") {
			if e["step"] == "save" {
				n++
			}
		}
		return n >= 2
	})
	for _, e := range a.events(t, "renew.failed") {
		if e["step"] == "save" && (e["file"] != blocker || e["check"] != "unwritable") {
			t.Errorf("renew.failed = %v, want file=%s check=unwritable", e, blocker)
		}
	}
	r.running(t)
	if !exists(t, filepath.Join(a.dir, conn.RenewKeyFile)) {
		t.Fatalf("%s was removed after a failed save", conn.RenewKeyFile)
	}
	if got := a.events(t, "cert.file_invalid"); len(got) != 1 || got[0]["check"] != "file" {
		t.Errorf("cert.file_invalid = %v, want one for the folder in its place", got)
	}
	if err := os.RemoveAll(blocker); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the renewal once the save works", func() bool {
		return len(a.events(t, "renew.completed")) == 1
	})
	keys := h.renewKeys()
	for i, k := range keys {
		if !k.Equal(keys[0]) {
			t.Fatalf("try %d sent another key", i+1)
		}
	}
	savedPair(t, a)
}

// The answer is checked on the certificates' own dates, not the agent's
// clock: with the agent's clock days behind the hub's, the certificate the
// hub just issued (an hour past its clock) starts in the agent's future,
// and is still saved.
func TestARenewalIsCheckedOnTheHubsDatesNotTheAgentsClock(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	dueHub(h)
	a := enrolled(t, h)
	startLoop(t, a, nil)
	waitFor(t, 10*time.Second, "the renewal", func() bool {
		return len(a.events(t, "renew.completed"))+len(a.events(t, "renew.failed")) > 0
	})
	if got := a.events(t, "renew.failed"); len(got) != 0 {
		t.Fatalf("renew.failed = %v for a certificate that starts in the agent's future", got)
	}
	if renewed := savedPair(t, a); !renewed.NotBefore.After(time.Now()) {
		t.Fatalf("the test's certificate starts %v, not after the agent's clock", renewed.NotBefore)
	}
}

// A lead that ends at the CA's end cannot be renewed to a later end: the
// due check logs renew.skipped{reason=ca_end} once for it, sends nothing
// and counts no invalid answer, however many checks run.
func TestALeadAtTheCAsEndIsSkippedOnce(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	caEnd := h.auth.Cert.NotAfter
	h.set(func(h *connectHub) { h.hubClock = func() time.Time { return caEnd.Add(-29 * 24 * time.Hour) } })
	a := enrolled(t, h)
	spec := machineCert()
	spec.notBefore, spec.notAfter = caEnd.Add(-90*24*time.Hour), caEnd
	writeFile(t, filepath.Join(a.dir, conn.CertFile), customPair(t, h, spec), 0o600)
	var checks *atomic.Int64
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		checks = countChecks(o)
	})
	h.connected(t, 1)
	waitChecks(t, checks, 10)
	r.running(t)
	if n := len(h.renewKeys()); n != 0 {
		t.Errorf("%d RenewCert calls for a lead at the CA's end, want 0", n)
	}
	if got := a.events(t, "renew.skipped"); len(got) != 1 || got[0]["reason"] != "ca_end" {
		t.Errorf("renew.skipped = %v, want one ca_end", got)
	}
	if got := metric(scrape(t, a), "fleet_agent_cert_renewals_total", `result="invalid"`); got != "" {
		t.Errorf("fleet_agent_cert_renewals_total{result=invalid} = %q, want none", got)
	}
}
