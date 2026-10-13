package tmux_test

import (
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// E13's read against real tmux: a person attached through a terminal and
// the agent's own control client each hold one row, told apart by the
// control flag, with the session and pane the key would land on.
func TestReadClientsListsPersonAndControlClient(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			person := startPerson(t, srv, "work", 100, 30)
			defer person.close()
			r := startClient(t, srv, nil)
			r.waitState(t, tmux.StateUp)

			var clients []tmux.ClientInfo
			deadline := time.Now().Add(wait)
			for {
				var err error
				clients, err = r.ReadClients(ctxT(t))
				if err != nil {
					t.Fatalf("ReadClients: %v", err)
				}
				if len(clients) == 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("ReadClients = %+v, want the person and the control client", clients)
				}
				time.Sleep(20 * time.Millisecond)
			}
			var personRow, controlRow *tmux.ClientInfo
			for i := range clients {
				if clients[i].Control {
					controlRow = &clients[i]
				} else {
					personRow = &clients[i]
				}
			}
			if personRow == nil || controlRow == nil {
				t.Fatalf("ReadClients = %+v, want one person row and one control row", clients)
			}
			if personRow.Session != 0 || personRow.Pane != 0 {
				t.Fatalf("person row = %+v, want session $0 pane %%0", personRow)
			}
			if time.Since(personRow.Activity) > 2*time.Minute {
				t.Fatalf("person activity = %v, want just now", personRow.Activity)
			}
			if controlRow.Session != 0 {
				t.Fatalf("control row = %+v, want it on session $0", controlRow)
			}
		})
	}
}

// The backup poll's stop condition: a subscription is unconfirmed on a new
// attach and confirmed once its first value arrives, on every attach.
func TestSubscriptionConfirmedTracksEachAttach(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "a", "exec cat")
			srv.NewSession(t, "-s", "b", "exec cat")
			n := newNotes()
			r := startClient(t, srv, n)
			r.waitState(t, tmux.StateUp)
			if r.SubscriptionConfirmed("nope") {
				t.Fatal("an unregistered subscription is confirmed")
			}
			if err := r.Subscribe(ctxT(t), tmux.Subscription{Name: "sess-act", Format: "#{S:#{session_id}=#{session_activity} }"}); err != nil {
				t.Fatal(err)
			}
			n.waitFor(t, 0, "the first subscription value", isSub("sess-act"))
			waitEvent(t, r.logs, "tmux.subscription_confirmed", map[string]any{"name": "sess-act"})
			if !r.SubscriptionConfirmed("sess-act") {
				t.Fatal("sess-act is not confirmed after its first value")
			}
			if r.SubscriptionConfirmed("other") {
				t.Fatal("an unregistered subscription is confirmed")
			}

			// A new attach confirms nothing until its own first value.
			srv.Run(t, "kill-session", "-t", "a")
			n.waitFor(t, 0, "%exit", func(m tmux.Message) bool { _, ok := m.(tmux.Exit); return ok })
			r.waitState(t, tmux.StateUp)
			waitTrue(t, "the re-attach to confirm the subscription again", func() bool {
				return r.SubscriptionConfirmed("sess-act")
			})
		})
	}
}
