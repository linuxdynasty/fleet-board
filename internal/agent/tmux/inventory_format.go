package tmux

import (
	"strconv"
	"strings"
)

// MaxFieldBytes bounds each text field of an inventory read (the names, the
// command and the @fleet_* options) in bytes, in tmux itself: tmux sends a
// longer value as empty, with its length beside it (ruling fork 1 on 7d1).
// tmux's #{=N:...} is no bound: it cuts by screen width, and zero-width
// characters and #[style] blocks pass it whole. With seven text fields a
// record is at most about 7.7 KiB, far under the parser's line bound. The
// inventory package still cuts a name to its 256 bytes (02e).
const MaxFieldBytes = 1024

// attachViewMark is the @fleet_internal value of an internal session: row
// 26's grouped attach view (02h, row-7 ruling fork 6). Only this exact
// string marks a session internal (ruling fork 2 on 7d1).
const attachViewMark = "attach_view"

// isAttachView reads 1 when the @fleet_internal option a format finds is
// exactly attachViewMark, else 0. The value itself never leaves tmux.
const isAttachView = "#{==:#{@fleet_internal}," + attachViewMark + "}"

// textField is one text field of an inventory read: the tmux format name and
// the closed field label of fleet_agent_tmux_inventory_fields_dropped_total.
type textField struct{ name, label string }

// textFields are the text fields of a record, in the order tmux sends them.
var textFields = [...]textField{
	{"@fleet_session_id", "fleet_session_id"},
	{"@fleet_owner", "fleet_owner"},
	{"@fleet_pane_key", "fleet_pane_key"},
	{"@fleet_goal_id", "fleet_goal_id"},
	{"session_name", "session_name"},
	{"window_name", "window_name"},
	{"pane_current_command", "pane_current_command"},
}

// Indexes into textFields.
const (
	textSessionID = iota
	textOwner
	textPaneKey
	textGoalID
	textSessionName
	textWindowName
	textCommand
)

// fixedFields are the ids, flags and sizes of a record, then the
// attach_view flag. No text a developer can set reaches them.
var fixedFields = []string{
	"#{session_id}", "#{session_created}", "#{session_attached}",
	"#{window_id}", "#{window_index}", "#{window_active}", "#{automatic-rename}",
	"#{pane_id}", "#{pane_index}", "#{pane_active}", "#{pane_dead}", "#{pane_dead_status}", "#{pane_width}", "#{pane_height}",
	isAttachView,
}

// canaryFormat must read "1": a tmux without the e|< comparison is a reply
// not in the asked shape, never a read whose every text field is blank.
const canaryFormat = "#{e|<:1,2}"

// Where each part of a record starts.
var (
	firstLength = len(fixedFields)
	canaryAt    = firstLength + len(textFields)
	firstText   = canaryAt + 1
)

// inventoryFields are a record's fields in order: the fixed fields, the byte
// length of each text field (#{n:...}), the canary, then each text field,
// sent only when its length is at most MaxFieldBytes. e|< compares numbers:
// a plain < compares strings ("10000" < "1025").
var inventoryFields = func() []string {
	out := append([]string(nil), fixedFields...)
	for _, f := range textFields {
		out = append(out, "#{n:"+f.name+"}")
	}
	out = append(out, canaryFormat)
	limit := strconv.Itoa(MaxFieldBytes + 1)
	for _, f := range textFields {
		out = append(out, "#{?#{e|<:#{n:"+f.name+"},"+limit+"},#{"+f.name+"},}")
	}
	return out
}()

// inventoryFormat is the list-panes -F format: each field after the marker,
// and one closing marker after the last field, so a newline in the last
// text field leaves its record short of markers and the join takes the
// next line (review of 7d1, round 2).
func inventoryFormat(marker string) string {
	var b strings.Builder
	for _, f := range inventoryFields {
		b.WriteString(marker + f)
	}
	b.WriteString(marker)
	return b.String()
}

// recordMarkers is how many markers a whole record holds: one before each
// field and the closing one.
var recordMarkers = len(inventoryFields) + 1

// The reasons a text field is dropped (the closed reason label).
const (
	dropOverBound = "over_bound" // longer than MaxFieldBytes: tmux sent it empty
	dropMismatch  = "mismatch"   // the bytes sent are not the length tmux gave
)

// fieldDrop is one text field left blank: its label, why, and the length
// tmux gave. Never the value.
type fieldDrop struct {
	field, reason string
	bytes         int
}

// boundedText checks text field i of a record against the length tmux sent
// beside it. A value of at most MaxFieldBytes must arrive whole and one
// over it empty; any other shape blanks this field only, so no text value
// can fail a read. A length that is not a number is not tmux's shape: ok
// is false.
func boundedText(i int, length, value string) (string, *fieldDrop, bool) {
	n64, err := strconv.ParseUint(length, 10, 31)
	if err != nil {
		return "", nil, false
	}
	n := int(n64)
	switch {
	case n > MaxFieldBytes && value == "":
		return "", &fieldDrop{field: textFields[i].label, reason: dropOverBound, bytes: n}, true
	case n > MaxFieldBytes || len(value) != n:
		return "", &fieldDrop{field: textFields[i].label, reason: dropMismatch, bytes: n}, true
	}
	return value, nil, true
}
