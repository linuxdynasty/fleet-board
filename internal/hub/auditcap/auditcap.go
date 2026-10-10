package auditcap

import (
	"sync"
	"time"
)

// The caps of defaults.md ("machine.enroll_failed audit rows", decided by
// the row 5 ruling, fork 12).
const (
	// Window is the span each cap counts in: whole minutes of the hub clock.
	Window = time.Minute
	// PerWindow is the most rows written in one window, all pairs together.
	PerWindow = 10
	// MaxTracked bounds each map the cap keeps (pairs carried to their next
	// row, distinct peers in a window, reasons), so a flood from many
	// addresses cannot grow memory. Past it, a pair's count is no longer
	// carried (the summary still counts it) and the distinct-peer count
	// stops at MaxTracked with PeersCapped set.
	MaxTracked = 1024
)

// Row is a refusal to write as its own audit row: its reason, its peer, and
// how many refusals of the same pair were suppressed since the pair's last
// row.
type Row struct {
	Reason     string
	Peer       string
	Suppressed int
}

// Summary accounts for the refusals of one window that got no row of their
// own.
type Summary struct {
	// Window is the start of the minute it covers.
	Window        time.Time
	Suppressed    int
	DistinctPeers int
	PeersCapped   bool
	// Reasons counts the suppressed refusals by reason.
	Reasons map[string]int
}

type pair struct{ reason, peer string }

// Cap decides which refused Enrolls get an audit row. It is safe for
// concurrent use; it does no I/O and holds its lock only for the decision.
// The zero value is not usable; call [New].
type Cap struct {
	mu      sync.Mutex
	window  time.Time         // start of the current window; zero before the first Note
	rows    int               // rows written in the current window
	written map[pair]struct{} // pairs with a row in the current window
	carry   map[pair]int      // suppressed per pair since its last row
	sum     *Summary          // the current window's suppressions, nil when none
	peers   map[string]struct{}
}

// New is a Cap with no history.
func New() *Cap {
	return &Cap{written: map[pair]struct{}{}, carry: map[pair]int{}, peers: map[string]struct{}{}}
}

// Note records one refusal at now. due is the summary of an earlier window
// that has not been taken yet (write it first); row is the refusal's own
// row, or nil when a cap suppresses it. A pair gets one row per window, and
// a window PerWindow rows in all. A now before the current window counts in
// the current window: an earlier window is never reopened.
func (c *Cap) Note(now time.Time, reason, peer string) (due *Summary, row *Row) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w := now.Truncate(Window); w.After(c.window) {
		due = c.take()
		c.window, c.rows = w, 0
		clear(c.written)
	}
	p := pair{reason, peer}
	if _, done := c.written[p]; !done && c.rows < PerWindow {
		c.rows++
		c.written[p] = struct{}{}
		n := c.carry[p]
		delete(c.carry, p)
		return due, &Row{Reason: reason, Peer: peer, Suppressed: n}
	}
	if _, ok := c.carry[p]; ok || len(c.carry) < MaxTracked {
		c.carry[p]++
	}
	if c.sum == nil {
		c.sum = &Summary{Window: c.window, Reasons: map[string]int{}}
	}
	c.sum.Suppressed++
	if _, ok := c.sum.Reasons[reason]; ok || len(c.sum.Reasons) < MaxTracked {
		c.sum.Reasons[reason]++
	}
	if _, ok := c.peers[peer]; !ok {
		if len(c.peers) < MaxTracked {
			c.peers[peer] = struct{}{}
		} else {
			c.sum.PeersCapped = true
		}
	}
	return due, nil
}

// Flush takes the current window's summary, or nil when nothing in it was
// suppressed. A stopping hub calls it so the minute in progress is not lost.
func (c *Cap) Flush() *Summary {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.take()
}

// take returns and clears the pending summary. Callers hold mu.
func (c *Cap) take() *Summary {
	s := c.sum
	if s == nil {
		return nil
	}
	s.DistinctPeers = len(c.peers)
	c.sum = nil
	clear(c.peers)
	return s
}
