package inventory

import (
	"context"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// structure is the subscription that makes a pane change anywhere on the
// server a read (coordinator decision on 7d, amending row-7 ruling fork
// 5(f)): tmux checks it about once a second and sends %subscription-changed
// when a session, window or pane appears or goes, or a pane dies. Measured
// on tmux 3.2a and 3.7c, a -d split, a kill of a non-active pane and a pane
// death in a session the control client is not attached to raise no notice
// of their own. A pane that lives under a second can be missed by it; the
// next rescan (Snapshot, SnapshotRequest, a client's state change) reads
// it. 7f measures the cost of the three subscriptions together (proof 9).
var structure = tmux.Subscription{
	Name:   "fleet-structure",
	Format: "#{S:#{session_id}:#{W:#{window_id}=#{P:#{pane_id}.#{pane_dead} }}}",
}

// readGap is the least time between two reads a notice asks for: a burst
// of notices and subscription values is one read a second at most. A
// Snapshot's read and a client's state change are not paced.
const readGap = time.Second

// triggers reports whether a control-mode message asks for a read: a
// window, session or client notice, %sessions-changed, %exit, or a new
// value of the structure subscription. Output, pause and continue, replies
// and other subscriptions (7e's, 7f's) do not.
func triggers(m tmux.Message) bool {
	switch m := m.(type) {
	case tmux.Output, tmux.Pause, tmux.Continue, tmux.Reply, tmux.Discarded:
		return false
	case tmux.SubscriptionChanged:
		return m.Name == structure.Name
	}
	return true
}

// pace waits until readGap has passed since last, serving Snapshot
// requests meanwhile (a Snapshot's read is fresh and covers the notice).
// It returns false when ctx ends, and true with served set when a Snapshot
// read happened while it waited.
func (s *server) pace(ctx context.Context, last time.Time) (ok, served bool) {
	wait := readGap - time.Since(last)
	if wait <= 0 {
		return true, false
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, served
		case <-t.C:
			return true, served
		case reply := <-s.snap:
			s.sync(ctx)
			reply <- s.level()
			served = true
		}
	}
}
