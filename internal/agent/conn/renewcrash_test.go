package conn_test

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// overdue is how long ago a certificate is issued so it is past two thirds
// of its 90-day life, and due for renewal at once.
const overdue = -61 * 24 * time.Hour

// A renewal stopped at each step, as a crash would stop it, ends whole on
// the next start, against the real hub: one renewed certificate and one
// new serial, never two. Before the call renew.key is kept and the next
// start sends that key; after the answer the hub has stored the
// certificate, and the same key gets it back; after the rename cert.pem is
// whole, so the next start uses it and removes renew.key as a leftover.
func TestARenewalStoppedAtAnyStepEndsWhole(t *testing.T) {
	t.Parallel()
	cases := []struct {
		step string
		// renewals is how many certificates the hub stored before the
		// restart; saved is whether cert.pem was there.
		renewals int
		saved    bool
	}{
		{step: "before_send"},
		{step: "after_answer", renewals: 1},
		{step: "after_rename", renewals: 1, saved: true},
	}
	for _, tc := range cases {
		t.Run(tc.step, func(t *testing.T) {
			t.Parallel()
			rh := startRenewHub(t, nil)
			a := rh.enrollAt(t, overdue)
			id, err := conn.LoadIdentity(a.dir)
			if err != nil {
				t.Fatal(err)
			}
			var hit atomic.Bool
			first := startLoop(t, a, func(o *conn.LoopOptions) {
				conn.SetRenewHook(o, func(step string) bool {
					if step == tc.step {
						hit.Store(true)
						return true
					}
					return false
				})
			})
			waitFor(t, 10*time.Second, "the renewal to reach "+tc.step, hit.Load)
			first.cancel()
			if err := first.result(t, 10*time.Second); err != nil {
				t.Fatalf("Run = %v", err)
			}
			if got := len(rh.Renewals(t, id.MachineID)); got != tc.renewals {
				t.Errorf("the hub stored %d renewals before the restart, want %d", got, tc.renewals)
			}
			if !exists(t, filepath.Join(a.dir, conn.RenewKeyFile)) {
				t.Fatalf("%s is gone after a stop at %s", conn.RenewKeyFile, tc.step)
			}
			if got := exists(t, filepath.Join(a.dir, conn.CertFile)); got != tc.saved {
				t.Fatalf("%s exists = %v after a stop at %s, want %v", conn.CertFile, got, tc.step, tc.saved)
			}

			startLoop(t, a, func(o *conn.LoopOptions) { o.RenewCheck = 10 * time.Millisecond })
			rh.fake.connected(t, 2)
			if tc.saved {
				time.Sleep(100 * time.Millisecond) // ten checks: none is due
			} else {
				waitFor(t, 10*time.Second, "the renewal after the restart", func() bool {
					return len(a.events(t, "renew.completed")) == 1
				})
			}
			renewed := savedPair(t, a)
			if got := rh.Renewals(t, id.MachineID); len(got) != 1 || got[0] != serialOf(renewed) {
				t.Errorf("the hub stored renewals %v, want one, cert.pem's %s", got, serialOf(renewed))
			}
			if got := rh.Serials(t, id.MachineID); len(got) != 2 {
				t.Errorf("the hub stored serials %v, want the enrolled one and one renewal", got)
			}
			if exists(t, filepath.Join(a.dir, conn.RenewKeyFile)) {
				t.Errorf("%s is left after the restart", conn.RenewKeyFile)
			}
			if !tc.saved {
				return
			}
			// The saved pair is used from the start.
			if got := rh.fake.calls.of("Connect")[1].serial; got != serialOf(renewed) {
				t.Errorf("the restarted agent presented serial %s, want the renewed %s", got, serialOf(renewed))
			}
			if len(a.events(t, "renew.leftover_removed")) != 1 {
				t.Errorf("no renew.leftover_removed after a stop at %s", tc.step)
			}
		})
	}
}
