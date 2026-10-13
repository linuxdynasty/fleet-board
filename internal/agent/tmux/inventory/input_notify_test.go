package inventory

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// Proof 8 (routing half): a %subscription-changed for an unknown name, or
// any other message, dirties no read. Only the structure subscription asks
// for an inventory read, only the activity one refreshes the activity
// times, and only the input one reads the clients.
func TestNotifyRoutesSubscriptions(t *testing.T) {
	t.Parallel()
	drain := func(ch chan struct{}) bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	cases := []struct {
		name           string
		msg            tmux.Message
		dirty, act, in bool
	}{
		{"structure subscription reads", tmux.SubscriptionChanged{Name: "fleet-structure"}, true, false, false},
		{"activity subscription refreshes", tmux.SubscriptionChanged{Name: "fleet-activity"}, false, true, false},
		{"input subscription reads the clients", tmux.SubscriptionChanged{Name: "fleet-input"}, false, false, true},
		{"an unknown subscription reads nothing", tmux.SubscriptionChanged{Name: "fleet-nope"}, false, false, false},
		{"output reads nothing", tmux.Output{Pane: 0, Data: []byte("x")}, false, false, false},
		{"a reply reads nothing", tmux.Reply{}, false, false, false},
		{"sessions-changed reads", tmux.SessionsChanged{}, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &server{
				o:     &Observer{},
				dirty: make(chan struct{}, 1), actDirty: make(chan struct{}, 1), inDirty: make(chan struct{}, 1),
			}
			s.notify(tc.msg)
			if got := drain(s.dirty); got != tc.dirty {
				t.Errorf("dirty = %v, want %v", got, tc.dirty)
			}
			if got := drain(s.actDirty); got != tc.act {
				t.Errorf("actDirty = %v, want %v", got, tc.act)
			}
			if got := drain(s.inDirty); got != tc.in {
				t.Errorf("inDirty = %v, want %v", got, tc.in)
			}
		})
	}
}
