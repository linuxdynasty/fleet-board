package inventory

import (
	"context"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// loop is the one goroutine that reads, diffs and emits for this server.
// A burst of notices coalesces into one read, and notices start a read at
// most once a second (readGap); nothing is held across a read or an emit.
// While a fresh-dead pane with no exit status is owed follow-ups, a timer
// starts one more read a deadStatusPollGap after the last one: the timer
// only fires when the death's own notice already asked for the status.
// Window-activity notices refresh only the activity times (no inventory
// read); the idle timer re-evaluates from the cached times when a server
// goes quiet, so idle edges are emitted with no new notice.
// Session-activity notices read only the clients (no inventory read):
// tmux's own about-once-a-second check paces them, and the buffered
// inDirty channel coalesces a burst into one. When the client (re)attaches,
// the loop reads the clients once against the last seen times, so a key
// pressed during the gap is still reported; a state change that finds the
// client up when it never saw it down (an ended and returned attach
// between two reads) reads once too. Until the input subscription
// confirms, a 2 s poll keeps the same read going, then stops (02h E13).
func (s *server) loop(ctx context.Context) {
	read := true
	wasUp := false
	var last time.Time
	var poll *time.Timer
	armed := func() <-chan time.Time {
		if poll == nil {
			return nil
		}
		return poll.C
	}
	stop := func() {
		if poll == nil {
			return
		}
		if !poll.Stop() {
			select {
			case <-poll.C:
			default:
			}
		}
		poll = nil
	}
	var idleT *time.Timer
	var idleC <-chan time.Time
	arm := func() {
		at, ok := s.track.nextIdle()
		if !ok {
			if idleT != nil && !idleT.Stop() {
				select {
				case <-idleT.C:
				default:
				}
			}
			idleC = nil
			return
		}
		d := time.Until(at)
		if d < 0 {
			d = 0
		}
		if idleT == nil {
			idleT = time.NewTimer(d)
		} else {
			if !idleT.Stop() {
				select {
				case <-idleT.C:
				default:
				}
			}
			idleT.Reset(d)
		}
		idleC = idleT.C
	}
	var inputT *time.Timer
	var inputC <-chan time.Time
	armInputPoll := func() {
		if inputT == nil {
			inputT = time.NewTimer(s.o.opts.pollGap())
		} else {
			inputT.Reset(s.o.opts.pollGap())
		}
		inputC = inputT.C
	}
	stopInputPoll := func() {
		if inputT != nil && !inputT.Stop() {
			select {
			case <-inputT.C:
			default:
			}
		}
		inputT, inputC = nil, nil
	}
	defer func() {
		if idleT != nil {
			idleT.Stop()
		}
		stopInputPoll()
	}()
	for {
		st, changed := s.c.Changed()
		if read {
			s.sync(ctx)
			last = time.Now()
			arm()
		}
		if up := st == tmux.StateUp; up && !wasUp {
			s.syncInput(ctx)
			armInputPoll()
		}
		wasUp = st == tmux.StateUp
		read = true
		stop()
		if len(s.pending) > 0 && s.c.State() == tmux.StateUp {
			poll = time.NewTimer(deadStatusPollGap)
		}
		select {
		case <-ctx.Done():
			stop()
			return
		case <-changed:
			if wasUp && s.c.State() == tmux.StateUp {
				// The attach ended and came back between two reads
				// of the state: compare the gap's keys and re-arm
				// the backup poll (a confirmed subscription stops
				// it at once).
				s.syncInput(ctx)
				armInputPoll()
			}
		case <-s.dirty:
			ok, _ := s.pace(ctx, last)
			if !ok {
				stop()
				return
			}
		case <-armed():
		case <-s.actDirty:
			ok, _ := s.pace(ctx, last)
			if !ok {
				return
			}
			s.syncActivity(ctx)
			arm()
		case <-s.inDirty:
			s.syncInput(ctx)
		case <-inputC:
			inputC = nil
			if s.c.State() == tmux.StateUp && !s.c.SubscriptionConfirmed(inputSub.Name) {
				s.syncInput(ctx)
				armInputPoll()
			} else {
				stopInputPoll()
			}
		case <-idleC:
			idleC = nil
			s.reapIdle(ctx)
			arm()
		case reply := <-s.snap:
			s.sync(ctx)
			last = time.Now()
			arm()
			reply <- s.level()
			read = false // just read
		}
	}
}
