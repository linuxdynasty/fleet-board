package inventory

import (
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// E13's credit rule, pinned without tmux: only a person counts (the
// control client never does), a client seen first counts, then only a
// moved key time counts, one event per pane per 10 s while the level keeps
// every key.
func TestInputTracker(t *testing.T) {
	t.Parallel()
	person := func(pid int, sec int64, pane tmux.PaneID) tmux.ClientInfo {
		return tmux.ClientInfo{PID: pid, Activity: time.Unix(sec, 0), Session: 0, Pane: pane}
	}
	control := func(pid int, sec int64) tmux.ClientInfo {
		return tmux.ClientInfo{PID: pid, Activity: time.Unix(sec, 0), Control: true, Session: 0, Pane: 0}
	}
	allow := func(string) bool { return true }
	deny := func(string) bool { return false }

	t.Run("empty read credits nothing", func(t *testing.T) {
		t.Parallel()
		at := time.Unix(1791889600, 0)
		tr := newInputTracker(func() time.Time { return at }, defaultInputGap)
		if got := tr.evaluate(nil, allow); len(got) != 0 {
			t.Fatalf("evaluate(nil) = %+v, want none", got)
		}
	})

	t.Run("the control client never counts", func(t *testing.T) {
		t.Parallel()
		at := time.Unix(1791889600, 0)
		tr := newInputTracker(func() time.Time { return at }, defaultInputGap)
		if got := tr.evaluate([]tmux.ClientInfo{control(9, 1791889600)}, allow); len(got) != 0 {
			t.Fatalf("evaluate(control) = %+v, want none", got)
		}
		if len(tr.last) != 0 {
			t.Fatalf("the control client is tracked: %+v", tr.last)
		}
	})

	t.Run("a new person counts at once", func(t *testing.T) {
		t.Parallel()
		at := time.Unix(1791889600, 0)
		tr := newInputTracker(func() time.Time { return at }, defaultInputGap)
		got := tr.evaluate([]tmux.ClientInfo{person(7, 1791889590, 0)}, allow)
		if len(got) != 1 || got[0].pane != "%0" || !got[0].at.Equal(time.Unix(1791889590, 0)) {
			t.Fatalf("evaluate(new person) = %+v, want one credit for %%0 at its key time", got)
		}
		if at := tr.input["%0"]; !at.Equal(time.Unix(1791889590, 0)) {
			t.Fatalf("level = %v, want the key time", at)
		}
	})

	t.Run("an unchanged key time credits nothing", func(t *testing.T) {
		t.Parallel()
		at := time.Unix(1791889600, 0)
		tr := newInputTracker(func() time.Time { return at }, defaultInputGap)
		cl := []tmux.ClientInfo{person(7, 1791889590, 0)}
		tr.evaluate(cl, allow)
		if got := tr.evaluate(cl, allow); len(got) != 0 {
			t.Fatalf("evaluate(same) = %+v, want none", got)
		}
	})

	t.Run("a moved key time is gated but still stamped", func(t *testing.T) {
		t.Parallel()
		at := time.Unix(1791889600, 0)
		tr := newInputTracker(func() time.Time { return at }, defaultInputGap)
		tr.evaluate([]tmux.ClientInfo{person(7, 1791889590, 0)}, allow)
		at = at.Add(3 * time.Second)
		got := tr.evaluate([]tmux.ClientInfo{person(7, 1791889595, 0)}, allow)
		if len(got) != 0 {
			t.Fatalf("evaluate(3 s later) = %+v, want none: one event per pane per 10 s", got)
		}
		if at := tr.input["%0"]; !at.Equal(time.Unix(1791889595, 0)) {
			t.Fatalf("level = %v, want the newer key time", at)
		}
		at = at.Add(8 * time.Second)
		got = tr.evaluate([]tmux.ClientInfo{person(7, 1791889600, 0)}, allow)
		if len(got) != 1 || got[0].pane != "%0" {
			t.Fatalf("evaluate(11 s later) = %+v, want one credit", got)
		}
	})

	t.Run("an unknown pane is skipped", func(t *testing.T) {
		t.Parallel()
		at := time.Unix(1791889600, 0)
		tr := newInputTracker(func() time.Time { return at }, defaultInputGap)
		if got := tr.evaluate([]tmux.ClientInfo{person(7, 1791889590, 5)}, deny); len(got) != 0 {
			t.Fatalf("evaluate(unknown pane) = %+v, want none", got)
		}
		if len(tr.input) != 0 {
			t.Fatalf("an unknown pane reached the level: %+v", tr.input)
		}
	})

	t.Run("a pane unknown then known still credits", func(t *testing.T) {
		t.Parallel()
		at := time.Unix(1791889600, 0)
		tr := newInputTracker(func() time.Time { return at }, defaultInputGap)
		cl := []tmux.ClientInfo{person(7, 1791889590, 0)}
		if got := tr.evaluate(cl, deny); len(got) != 0 {
			t.Fatalf("evaluate(unknown) = %+v, want none", got)
		}
		// The inventory catches up (the structure notice's own read):
		// the client was not baselined while unknown, so the key that
		// announced the session still counts.
		got := tr.evaluate(cl, allow)
		if len(got) != 1 || got[0].pane != "%0" {
			t.Fatalf("evaluate(known) = %+v, want one credit", got)
		}
	})

	t.Run("a departed client that returns counts again", func(t *testing.T) {
		t.Parallel()
		at := time.Unix(1791889600, 0)
		tr := newInputTracker(func() time.Time { return at }, defaultInputGap)
		tr.evaluate([]tmux.ClientInfo{person(7, 1791889590, 0)}, allow)
		tr.evaluate(nil, allow)
		if len(tr.last) != 0 {
			t.Fatalf("a departed client is kept: %+v", tr.last)
		}
		at = at.Add(11 * time.Second)
		got := tr.evaluate([]tmux.ClientInfo{person(7, 1791889590, 0)}, allow)
		if len(got) != 1 {
			t.Fatalf("evaluate(returned) = %+v, want one credit: first seen counts", got)
		}
	})

	t.Run("two clients at one pane are one credit at the later key", func(t *testing.T) {
		t.Parallel()
		at := time.Unix(1791889600, 0)
		tr := newInputTracker(func() time.Time { return at }, defaultInputGap)
		got := tr.evaluate([]tmux.ClientInfo{person(7, 1791889590, 0), person(8, 1791889599, 0)}, allow)
		if len(got) != 1 || got[0].pane != "%0" {
			t.Fatalf("evaluate(two at %%0) = %+v, want one credit", got)
		}
		if at := tr.input["%0"]; !at.Equal(time.Unix(1791889599, 0)) {
			t.Fatalf("level = %v, want the later key time", at)
		}
	})
}

// The live tests shorten the gate, but what ships is the contract: one
// PaneInput per pane per 10 s (events.proto, 02h E13).
func TestInputGapDefault(t *testing.T) {
	t.Parallel()
	if got := (&Options{}).inputEmitGap(); got != 10*time.Second {
		t.Fatalf("default input gate = %v, want 10s", got)
	}
	if defaultInputGap != 10*time.Second {
		t.Fatalf("defaultInputGap = %v, want 10s", defaultInputGap)
	}
}
