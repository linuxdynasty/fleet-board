package tmux_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// The text fields of a list-panes record, in order.
const (
	tSessionID = iota
	tOwner
	tPaneKey
	tGoalID
	tSessionName
	tWindowName
	tCommand
	nText
)

// rec is one list-panes record to forge: the pane id, the seven text
// values as tmux sends them, the byte counts tmux sends beside them, and
// the canary.
type rec struct {
	pane   string
	text   [nText]string
	n      [nText]int
	canary string
}

// newRec is a record whose counts match its text, as tmux sends it.
func newRec(pane string, text [nText]string) rec {
	r := rec{pane: pane, text: text, canary: "1"}
	for i, v := range text {
		r.n[i] = len(v)
	}
	return r
}

// line builds the record as tmux sends it: a marker before each field (ids,
// flags and sizes, the attach_view flag, the counts, the canary, then the
// text) and a closing marker.
func (r rec) line(marker string) string {
	fields := []string{"$0", "1790000000", "0", "@0", "0", "1", "0", r.pane, "0", "1", "0", "", "80", "24", "0"}
	for _, n := range r.n {
		fields = append(fields, strconv.Itoa(n))
	}
	fields = append(fields, r.canary)
	fields = append(fields, r.text[:]...)
	if len(fields) != tmux.InventoryFieldCountForTest() {
		panic("rec: field count")
	}
	return marker + strings.Join(fields, marker) + marker
}

// record builds one list-panes line with goal as the @fleet_goal_id value.
func record(marker, pane, goal string) string {
	return newRec(pane, [nText]string{tGoalID: goal, tSessionName: "work", tWindowName: "zsh", tCommand: "zsh"}).line(marker)
}

// A value with newlines anywhere, including at its end (so the next field,
// marker first, starts a line), is joined back into its own record: the
// read never fails on it (review of 7d1: "end\n", "\n\n\n", "a\rb\r\n").
func TestInventoryRecordsSurviveNewlinesInValues(t *testing.T) {
	t.Parallel()
	const marker = "~0123456789abcdef~"
	for _, v := range []string{"end\n", "\n\n\n", "a\rb\r\n", "x\n", "\n\n", "mid\ndle", "plain"} {
		body := strings.Split(record(marker, "%1", v)+"\n"+record(marker, "%2", "g"), "\n")
		panes, drops, ok := tmux.InventoryRecordsForTest(body, marker)
		if !ok || len(panes) != 2 || len(drops) != 0 {
			t.Fatalf("value %q: ok %v, %d panes, drops %v; want 2 panes, no drop", v, ok, len(panes), drops)
		}
		if panes[0].Tags.GoalID != v || panes[1].ID.String() != "%2" {
			t.Fatalf("value %q read as %q; second pane %s", v, panes[0].Tags.GoalID, panes[1].ID)
		}
	}
}

// Each text field is bounded in bytes: tmux sends a value of at most
// MaxFieldBytes whole and a longer one empty, with the length beside it.
// A record whose length and bytes disagree loses that field only (counted
// as a mismatch); no text value can fail the read. A tmux that does not
// answer the canary is not in the asked shape.
func TestParseInventoryLineFieldBounds(t *testing.T) {
	t.Parallel()
	const marker = "~0123456789abcdef~"
	long := func(n int) string { return strings.Repeat("y", n) }
	base := [nText]string{tSessionName: "work", tWindowName: "zsh", tCommand: "zsh"}
	sent := func(field, n int) rec { // a value of n bytes as tmux sends it
		text := base
		if n <= tmux.MaxFieldBytes {
			text[field] = long(n)
		}
		r := newRec("%1", text)
		r.n[field] = n
		return r
	}
	forged := func(field, n int, v string) rec {
		text := base
		text[field] = v
		r := newRec("%1", text)
		r.n[field] = n
		return r
	}
	for _, tc := range []struct {
		name     string
		rec      rec
		keep     string // the owner field read, when the case is on it
		wantDrop *tmux.FieldDropForTest
	}{
		{"1024 bytes kept", sent(tOwner, 1024), long(1024), nil},
		{"1025 bytes dropped", sent(tOwner, 1025), "", &tmux.FieldDropForTest{Field: "fleet_owner", Reason: "over_bound", Bytes: 1025}},
		{"999 bytes kept", sent(tOwner, 999), long(999), nil},
		{"10000 bytes dropped", sent(tOwner, 10000), "", &tmux.FieldDropForTest{Field: "fleet_owner", Reason: "over_bound", Bytes: 10000}},
		{"short value, wrong count", forged(tOwner, 5, "abc"), "", &tmux.FieldDropForTest{Field: "fleet_owner", Reason: "mismatch", Bytes: 5}},
		{"over the bound but sent", forged(tOwner, 2000, "abc"), "", &tmux.FieldDropForTest{Field: "fleet_owner", Reason: "mismatch", Bytes: 2000}},
		{"window name, wrong count", forged(tWindowName, 1, "zsh"), "", &tmux.FieldDropForTest{Field: "window_name", Reason: "mismatch", Bytes: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := strings.Split(tc.rec.line(marker)+"\n"+record(marker, "%2", "g"), "\n")
			panes, drops, ok := tmux.InventoryRecordsForTest(body, marker)
			if !ok || len(panes) != 2 {
				t.Fatalf("ok %v, %d panes; want the read to stand with 2", ok, len(panes))
			}
			if panes[0].Tags.Owner != tc.keep || panes[1].Tags.GoalID != "g" {
				t.Fatalf("owner = %d bytes, want %d; second pane goal %q", len(panes[0].Tags.Owner), len(tc.keep), panes[1].Tags.GoalID)
			}
			if tc.wantDrop == nil {
				if len(drops) != 0 {
					t.Fatalf("drops = %v, want none", drops)
				}
				return
			}
			if len(drops) != 1 || drops[0] != *tc.wantDrop {
				t.Fatalf("drops = %v, want [%v]", drops, *tc.wantDrop)
			}
			if tc.wantDrop.Field == "window_name" && panes[0].WindowName != "" {
				t.Fatalf("window name = %q, want blank", panes[0].WindowName)
			}
		})
	}
	noCanary := newRec("%1", base)
	noCanary.canary = "0"
	if _, _, ok := tmux.InventoryRecordsForTest([]string{noCanary.line(marker)}, marker); ok {
		t.Fatal("a canary of 0 was read; want the reply refused")
	}
}
