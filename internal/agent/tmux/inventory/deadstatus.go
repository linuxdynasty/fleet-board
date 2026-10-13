package inventory

import (
	"context"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// deadStatusPollGap is how soon after a read that saw a fresh-dead pane
// with no exit status the loop reads again, and deadStatusPollMax how many
// such follow-ups one pane is owed: thirty one-second reads. tmux can
// report the death before waitpid hands it the status, and on Linux no
// notice follows when only the status lands, so without these the hub
// would never learn it. But a Linux tmux can also lose the status on the
// way (measured: the pane reaped with neither status nor signal, and no
// read in 30 s ever showed them): then the status-less remains death
// already reported is tmux's whole answer. A signal death never gains a
// status either: the cap bounds that waste to thirty extra reads, counted
// expired. The death's own notice asked for the status; the timer only
// delays what that notice already asked for, like readGap.
const (
	deadStatusPollGap = time.Second
	deadStatusPollMax = 30
)

// watchDeadStatus owes follow-ups to every pane this diff reported dead
// with no exit status yet (a status-less remains PaneExited): tmux can
// report the death before it knows the status. A pane first seen dead is
// not watched: had it exited, its status would already be there, so a
// status-less one is a signal death with no status coming.
func (s *server) watchDeadStatus(evs []*agentv1.Event) {
	for _, ev := range evs {
		pe := ev.GetPaneExited()
		if pe == nil || !pe.GetRemains() || pe.ExitStatus != nil {
			continue
		}
		if s.pending == nil {
			s.pending = map[string]int{}
		}
		if id := pe.GetPane().GetPaneId(); id != "" {
			if _, ok := s.pending[id]; !ok {
				s.pending[id] = deadStatusPollMax
			}
		}
	}
}

// reapDeadStatus settles waits against the latest inventory: a pane with a
// status now counts landed, one tmux dropped meanwhile is dropped with its
// remains:false exit, and one still status-less loses one owed follow-up,
// counting expired at zero. Every read settles, whatever started it.
func (s *server) reapDeadStatus(ctx context.Context) {
	if len(s.pending) == 0 || s.last == nil {
		return
	}
	panes := index(s.last).panes
	for id, left := range s.pending {
		p, ok := panes[id]
		switch {
		case !ok:
			delete(s.pending, id)
		case p.ExitStatus != nil:
			delete(s.pending, id)
			s.o.tel.deadStatusDone(ctx, s.kind, "landed")
		case left <= 1:
			delete(s.pending, id)
			s.o.tel.deadStatusDone(ctx, s.kind, "expired")
		default:
			s.pending[id] = left - 1
		}
	}
}

// clearDeadStatus drops every wait: the server went away or restarted, so
// no owed follow-up can settle it.
func (s *server) clearDeadStatus() {
	for id := range s.pending {
		delete(s.pending, id)
	}
}
