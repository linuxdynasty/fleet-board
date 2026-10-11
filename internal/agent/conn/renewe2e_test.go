package conn_test

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// The certificate is renewed at two thirds of its life by hub time,
// against the real hub handler: a minute before that point no check sends
// anything; a minute after it, the next hourly check renews over the
// stream's own HTTP/2 connection while the stream stays up, identity.json
// stays byte for byte, and the next handshake presents the new serial.
func TestRenewAtTwoThirdsEndToEnd(t *testing.T) {
	t.Parallel()
	rh := startRenewHub(t, nil)
	// Issued so that two thirds of its life fall two minutes from now.
	a := rh.enrollAt(t, -twoThirds+5*time.Minute+2*time.Minute)
	id, err := conn.LoadIdentity(a.dir)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(a.dir, conn.IdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	old := enrolledLeaf(t, a)
	hub := &skewClock{} // moves hub time on within the stream
	var checks *atomic.Int64
	r := startLoop(t, a, func(o *conn.LoopOptions) {
		o.RenewCheck = 10 * time.Millisecond
		conn.SetElapsed(o, hub.since)
		checks = countChecks(o)
	})
	rh.fake.connected(t, 1)
	// A minute before day 60 of the 90-day life: ten checks send nothing.
	hub.set(time.Until(old.NotBefore.Add(twoThirds - time.Minute)))
	from := checks.Load()
	waitFor(t, 10*time.Second, "ten checks", func() bool { return checks.Load() >= from+10 })
	if n := len(rh.fake.calls.of("RenewCert")); n != 0 {
		t.Fatalf("%d RenewCert calls before two thirds of the life, want 0", n)
	}
	// A minute after it: the next check renews.
	hub.set(time.Until(old.NotBefore.Add(twoThirds + time.Minute)))
	waitFor(t, 10*time.Second, "the renewal", func() bool {
		return len(a.events(t, "renew.completed")) == 1
	})
	r.running(t)
	// renew.completed is logged just before the renewal's key file is
	// removed, so wait for the removal instead of checking it at once.
	keyFile := filepath.Join(a.dir, conn.RenewKeyFile)
	waitFor(t, 10*time.Second, conn.RenewKeyFile+" to be removed after the renewal", func() bool { return !exists(t, keyFile) })
	renewed := savedPair(t, a)
	if !renewed.NotAfter.After(old.NotAfter) || serialOf(renewed) == serialOf(old) {
		t.Fatalf("renewed certificate %s ends %v; the enrolled one %s ends %v", serialOf(renewed), renewed.NotAfter,
			serialOf(old), old.NotAfter)
	}
	if got := rh.Renewals(t, id.MachineID); len(got) != 1 || got[0] != serialOf(renewed) {
		t.Errorf("the hub stored renewals %v, want [%s]", got, serialOf(renewed))
	}
	if got := rh.Serials(t, id.MachineID); len(got) != 2 {
		t.Errorf("the hub stored serials %v, want the enrolled one and one renewal", got)
	}
	after, err := os.ReadFile(filepath.Join(a.dir, conn.IdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("identity.json changed during the renewal")
	}
	// The stream that was up when the renewal ran is still the only one,
	// and RenewCert used its connection (Connect-go reuses the HTTP/2
	// connection for a unary call on the same transport).
	if n := rh.fake.count(); n != 1 || rh.fake.stream(1).ended {
		t.Fatalf("%d streams, the first ended %v: the renewal dropped the stream", n, rh.fake.stream(1).ended)
	}
	connects, renews := rh.fake.calls.of("Connect"), rh.fake.calls.of("RenewCert")
	if len(renews) != 1 || renews[0].remote != connects[0].remote || renews[0].serial != serialOf(old) {
		t.Errorf("RenewCert calls %+v; the stream's %+v", renews, connects[0])
	}
	// The next handshake presents the renewed certificate.
	rh.fake.dropLive(t)
	rh.fake.connected(t, 2)
	connects = rh.fake.calls.of("Connect")
	if got := connects[len(connects)-1]; got.serial != serialOf(renewed) || got.remote == connects[0].remote {
		t.Errorf("the next stream %+v, want a new connection with serial %s", got, serialOf(renewed))
	}
	m := scrape(t, a)
	if got := metric(m, "fleet_agent_cert_renewals_total", `result="renewed"`); got != "1" {
		t.Errorf("fleet_agent_cert_renewals_total{result=renewed} = %q, want 1", got)
	}
	exp, err := strconv.ParseFloat(metric(m, "fleet_agent_cert_expiry_seconds"), 64)
	want := renewed.NotAfter.Sub(time.Now().Add(time.Duration(hub.off.Load()))).Seconds() // by hub time
	if err != nil || math.Abs(exp-want) > 60 {
		t.Errorf("fleet_agent_cert_expiry_seconds = %v (%v), want about %v (the renewed pair)", exp, err, want)
	}
}
