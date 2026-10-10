package conn

import (
	"context"
	"crypto/x509"
	"sync"
	"time"
)

// renewGate decides whether a due renewal may start. Run's goroutine asks
// (claim); the renewal goroutine reports back (done). One renewal runs at
// a time; none starts within one check interval of the last that
// completed (a reconnect checks too, so this bounds renewals to one per
// interval); none starts for a lead that was already due by hub time the
// moment it was saved (its hub signs on another clock than the one its
// HelloAck reports: renewing again would mint a new serial at every
// check), nor for a lead that ends at the CA's end (no answer can end
// later). A skipped lead is logged once.
type renewGate struct {
	mu      sync.Mutex
	running bool
	// last is when the last renewal completed, by the monotonic clock.
	last time.Time
	// stuck is the serial of a lead that was due when it was saved.
	stuck string
	// logged is the serial and reason last logged as renew.skipped.
	logged string
}

// Reasons a due renewal does not start, for renew.skipped.
const (
	skipCAEnd     = "ca_end"
	skipDueAtOnce = "due_at_once"
)

// claim reports whether a renewal of lead may start now and, if so, marks
// one running. skip is set when it may not and the owner should know.
func (g *renewGate) claim(lead *certPair, ca *x509.Certificate, interval time.Duration) (ok bool, skip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.running:
		return false, ""
	case !lead.notAfter().Before(ca.NotAfter):
		return false, skipCAEnd
	case g.stuck == lead.serial():
		return false, skipDueAtOnce
	case !g.last.IsZero() && time.Since(g.last) < interval:
		return false, ""
	}
	g.running = true
	return true, ""
}

// unstick clears a due_at_once mark on lead once it is not due: a hub
// whose HelloAck clock was off when it signed shows it young again on a
// later HelloAck, so its true two-thirds point counts again. A hub whose
// clocks disagree for good keeps the lead looking due, so it stays marked.
func (g *renewGate) unstick(lead *certPair) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stuck == lead.serial() {
		g.stuck, g.logged = "", ""
	}
}

// firstSkip reports whether this lead and reason were not logged yet.
func (g *renewGate) firstSkip(lead *certPair, reason string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := lead.serial() + "/" + reason
	if g.logged == key {
		return false
	}
	g.logged = key
	return true
}

// done ends the running renewal; saved is the new lead when it completed,
// and dueAtOnce whether that lead was already due by hub time.
func (g *renewGate) done(saved *certPair, dueAtOnce bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running = false
	if saved == nil {
		return
	}
	g.last = time.Now()
	if dueAtOnce {
		g.stuck = saved.serial()
	}
}

// logSkip logs renew.skipped once per lead and reason.
func (l *Loop) logSkip(ctx context.Context, lead *certPair, reason string) {
	if l.gate.firstSkip(lead, reason) {
		l.log.WarnContext(ctx, "renew.skipped", "reason", reason, "serial", lead.serial(),
			"not_after", lead.notAfter().UTC())
	}
}
