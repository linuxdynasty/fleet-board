package conn_test

import (
	"bytes"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// twoThirds is two thirds of the 90-day agent certificate life, written
// out rather than computed the way the code computes it.
const twoThirds = 60 * 24 * time.Hour

// A renewed pair is renewed again at two thirds of its own life by hub
// time, against the real hub: the second renewal goes over a connection
// that presents the first renewed serial, and replaces cert.pem. The hub
// signs on its own clock, so the second pair starts in the agent's future
// and is not presented until it starts.
func TestASecondRenewalReplacesTheFirstEndToEnd(t *testing.T) {
	t.Parallel()
	rh := startRenewHub(t, nil)
	a := rh.enrollAt(t, overdue)
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	hub := &skewClock{}
	var checks *atomic.Int64
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		conn.SetElapsed(o, hub.since)
		checks = countChecks(o)
	})
	waitFor(t, 10*time.Second, "the first renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	first := savedPair(t, a)
	rh.fake.dropLive(t)
	rh.fake.connected(t, 2)
	if got := rh.fake.calls.of("Connect")[1].serial; got != serialOf(first) {
		t.Fatalf("stream 2 presented %s, want the first renewed serial %s", got, serialOf(first))
	}
	from := checks.Load()
	waitFor(t, 10*time.Second, "ten checks", func() bool { return checks.Load() >= from+10 })
	if n := len(rh.fake.calls.of("RenewCert")); n != 1 {
		t.Fatalf("%d RenewCert calls before the renewed pair is due, want 1", n)
	}
	// Day 60 of the first renewed pair's life, plus a minute, on the hub.
	step := time.Until(first.NotBefore.Add(twoThirds + time.Minute))
	rh.clock.set(step)
	hub.set(step)
	waitFor(t, 10*time.Second, "the second renewal", func() bool { return len(a.events(t, "renew.completed")) == 2 })
	second := savedPair(t, a)
	if serialOf(second) == serialOf(first) || !second.NotAfter.After(first.NotAfter) {
		t.Fatalf("cert.pem still holds the first renewal (%s)", serialOf(second))
	}
	renews := rh.fake.calls.of("RenewCert")
	if len(renews) != 2 || renews[1].serial != serialOf(first) {
		t.Errorf("RenewCert calls %+v, want the second from the first renewed serial", renews)
	}
	if got := rh.Renewals(t, id.MachineID); len(got) != 2 || got[1] != serialOf(second) {
		t.Errorf("the hub stored renewals %v, want two ending with %s", got, serialOf(second))
	}
	if got := rh.Serials(t, id.MachineID); len(got) != 3 {
		t.Errorf("the hub stored serials %v, want three", got)
	}
	if exists(t, filepath.Join(a.dir, conn.RenewKeyFile)) {
		t.Errorf("%s is left after the second renewal", conn.RenewKeyFile)
	}
	// cert.pem holds one pair, so the first renewed pair is gone; until the
	// second starts by the agent's clock the enrolled pair, still inside
	// its life here, is the one presented.
	rh.fake.dropLive(t)
	rh.fake.connected(t, 3)
	if got, want := rh.fake.calls.of("Connect")[2].serial, serialOf(enrolledLeaf(t, a)); got != want {
		t.Errorf("stream 3 presented %s, want the enrolled %s until the second pair starts", got, want)
	}
}

// A cert.pem already there, valid and in use or not valid at all, is
// replaced by the next renewal.
func TestARenewalReplacesTheCertFileThere(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		valid bool
	}{
		{name: "a valid pair in use", valid: true},
		{name: "a file that is not a pair"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			data := []byte("not a pair\n")
			if tc.valid {
				data, _, _ = pairFile(t, h.auth, fakeMachine, time.Now().Add(soon))
			}
			path := filepath.Join(a.dir, conn.CertFile)
			writeFile(t, path, data, 0o600)
			dueHub(h)
			startLoop(t, a, nil)
			waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
			after, err := os.ReadFile(path) //nolint:gosec // G304: the test's own temp folder
			if err != nil || bytes.Equal(after, data) {
				t.Fatalf("%s was not replaced (%v)", conn.CertFile, err)
			}
			if got := savedPair(t, a); !h.renewKeys()[0].Equal(got.PublicKey) {
				t.Error("cert.pem does not hold the renewed certificate")
			}
		})
	}
}
