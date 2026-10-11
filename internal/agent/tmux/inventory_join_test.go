package tmux_test

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// record builds one list-panes line: valid ids and numbers, then the five
// tag fields and three names, with goal as the @fleet_goal_id value.
func record(marker, pane, goal string) string {
	fields := []string{
		"$0", "1790000000", "0", "@0", "0", "1", "0", pane, "0", "1", "0", "", "80", "24",
		"", "", "", "", goal, "work", "zsh", "zsh",
	}
	if len(fields) != tmux.InventoryFieldCountForTest() {
		panic("record: field count")
	}
	return marker + strings.Join(fields, marker)
}

// A value with newlines anywhere, including at its end (so the next field,
// marker first, starts a line), is joined back into its own record: the
// read never fails on it (review of 7d1: "end\n", "\n\n\n", "a\rb\r\n").
func TestInventoryRecordsSurviveNewlinesInValues(t *testing.T) {
	t.Parallel()
	const marker = "~0123456789abcdef~"
	for _, v := range []string{"end\n", "\n\n\n", "a\rb\r\n", "x\n", "\n\n", "mid\ndle", "plain"} {
		body := strings.Split(record(marker, "%1", v)+"\n"+record(marker, "%2", "g"), "\n")
		panes, ok := tmux.InventoryRecordsForTest(body, marker)
		if !ok || len(panes) != 2 {
			t.Fatalf("value %q: ok %v, %d panes, want 2", v, ok, len(panes))
		}
		if panes[0].Tags.GoalID != v || panes[1].ID.String() != "%2" {
			t.Fatalf("value %q read as %q; second pane %s", v, panes[0].Tags.GoalID, panes[1].ID)
		}
	}
}
