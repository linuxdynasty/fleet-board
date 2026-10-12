package inventory

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// The tracker is pure: a fake clock drives it, no tmux. Pane ids are
// "%1"-shaped, windows "@1"-shaped, as the read reports them.
func TestActivityTracker(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	now := base
	tr := newActivityTracker(func() time.Time { return now }, 2*time.Minute)

	panes := func() []paneOutlet {
		return []paneOutlet{{pane: "%1", window: "@1", lastWindow: base, dead: false}}
	}

	t.Run("first sight initializes silently", func(t *testing.T) {
		if ch := tr.evaluate(panes()); len(ch) != 0 {
			t.Fatalf("first evaluate = %v, want no transitions", ch)
		}
	})
	t.Run("new window activity makes a quiet pane active", func(t *testing.T) {
		now = base.Add(3 * time.Minute)
		_ = tr.evaluate(panes()) // goes idle first: window time is stale
		now = base.Add(4 * time.Minute)
		fresh := []paneOutlet{{pane: "%1", window: "@1", lastWindow: now, dead: false}}
		ch := tr.evaluate(fresh)
		if len(ch) != 1 || ch[0].from != typev1.Activity_ACTIVITY_IDLE || ch[0].to != typev1.Activity_ACTIVITY_ACTIVE {
			t.Fatalf("fresh window activity = %v, want one idle->active", ch)
		}
		if !ch[0].lastOut.Equal(now) {
			t.Fatalf("transition lastOut = %v, want %v", ch[0].lastOut, now)
		}
	})
	t.Run("quiet past the edge goes idle", func(t *testing.T) {
		now = now.Add(3 * time.Minute)
		ch := tr.evaluate(panes())
		if len(ch) != 1 || ch[0].from != typev1.Activity_ACTIVITY_ACTIVE || ch[0].to != typev1.Activity_ACTIVITY_IDLE {
			t.Fatalf("quiet = %v, want one active->idle", ch)
		}
	})
	t.Run("steady state emits nothing", func(t *testing.T) {
		now = now.Add(time.Minute)
		if ch := tr.evaluate(panes()); len(ch) != 0 {
			t.Fatalf("steady evaluate = %v, want none", ch)
		}
	})
	t.Run("dead panes are forgotten without a transition", func(t *testing.T) {
		dead := []paneOutlet{{pane: "%1", window: "@1", lastWindow: now, dead: true}}
		if ch := tr.evaluate(dead); len(ch) != 0 {
			t.Fatalf("dead evaluate = %v, want none", ch)
		}
		fresh := []paneOutlet{{pane: "%1", window: "@1", lastWindow: now, dead: false}}
		if ch := tr.evaluate(fresh); len(ch) != 0 {
			t.Fatalf("revived evaluate = %v, want silent re-init", ch)
		}
	})
	t.Run("gone panes are forgotten", func(t *testing.T) {
		if ch := tr.evaluate(nil); len(ch) != 0 {
			t.Fatalf("empty evaluate = %v, want none", ch)
		}
		if ch := tr.evaluate(panes()); len(ch) != 0 {
			t.Fatalf("returned pane = %v, want silent re-init", ch)
		}
	})
}

// Activity and output time ride in the Snapshot and the E4 events, never
// in PaneChanged: the contract says they have their own events (E1's
// PaneChanged comment in events.proto).
func TestDiffIgnoresActivity(t *testing.T) {
	t.Parallel()
	mk := func() *agentv1.TmuxServer {
		inv := tmux.Inventory{
			StartID:  "s",
			Sessions: []tmux.SessionInfo{{ID: 0, Name: "work"}},
			Panes:    []tmux.PaneInfo{{Session: 0, Window: 0, ID: 0}},
		}
		return buildServer("default", inv, "3.7c")
	}
	old, next := mk(), mk()
	p := next.GetSessions()[0].GetWindows()[0].GetPanes()[0]
	p.Activity = typev1.Activity_ACTIVITY_IDLE
	p.LastOutputTime = timestamppb.Now()
	if got := describe(diff(old, next)); got != "" {
		t.Fatalf("activity-only diff = %q, want no events", got)
	}
}

func TestParsePaneID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		id   tmux.PaneID
		want bool
	}{
		{"%0", 0, true},
		{"%12", 12, true},
		{"0", 0, false},
		{"%", 0, false},
		{"%@0", 0, false},
		{"%-1", 0, false},
		{"%01", 0, false},
		{"", 0, false},
		{"default", 0, false},
	}
	for _, tc := range tests {
		t.Run("id "+tc.in, func(t *testing.T) {
			t.Parallel()
			id, ok := parsePaneID(tc.in)
			if ok != tc.want || (ok && id != tc.id) {
				t.Fatalf("parsePaneID(%q) = (%v, %v), want (%v, %v)", tc.in, id, ok, tc.id, tc.want)
			}
		})
	}
}

func TestActivitySummaryPacing(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_700_000_000, 0)
	if !dueSummary(map[string]time.Time{}, "%1", base) {
		t.Fatal("first summary is due")
	}
	last := map[string]time.Time{"%1": base}
	if dueSummary(last, "%1", base.Add(4*time.Second)) {
		t.Fatal("summary 4 s after the last is not due")
	}
	if !dueSummary(last, "%1", base.Add(5*time.Second)) {
		t.Fatal("summary 5 s after the last is due")
	}
	if !dueSummary(last, "%2", base) {
		t.Fatal("a pane never summarized is due")
	}
}
