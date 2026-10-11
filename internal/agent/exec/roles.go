package exec

import (
	"strings"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// roleRow is one row of the 02g matrix "What each principal may ask an agent
// to do", copied verbatim (TestRoleMatrixMatchesTheSpec compares it with the
// spec). cells are the VIEWER, OPERATOR, OWNER, SCHEDULER and ARBITER
// columns.
type roleRow struct {
	doc      string
	kinds    []string
	variants []string // "" matches a command with no variant
	cells    [5]string
}

const no = "—"

// roleMatrix is 02g's table. A kind with no row (EnsureScratch,
// CollectScratch) or a variant with no row (a SendKeys purpose or a
// RunHarnessCommand class not listed) is refused.
var roleMatrix = []roleRow{
	{"`EnsureProbe` (`run_now` from `ProbeAccount`)", []string{"EnsureProbe"}, nil, [5]string{no, "A", "A", "A", no}},
	{"`StopProbe`", []string{"StopProbe"}, nil, [5]string{no, no, "A", "A", no}},
	{"`Preflight`, `EnsureSession`, `EnsureWindow`, `EnsureWorktree`", []string{"Preflight", "EnsureSession", "EnsureWindow", "EnsureWorktree"}, nil, [5]string{no, "A", "A", "A", no}},
	{"`StartGoal` (incl. `model`, `effort` steps; class SESSION)", []string{"StartGoal"}, nil, [5]string{no, "A", "A", "A", no}},
	{"`StartGoal` with `fresh_context` (types `/clear`)", []string{"StartGoal"}, []string{"fresh_context"}, [5]string{no, "A, F*", "A, F*", "A, F*", no}},
	{"`SendKeys` purpose `OPERATOR_TEXT`", []string{"SendKeys"}, []string{"OPERATOR_TEXT"}, [5]string{no, "L", "L", no, no}},
	{"`SendKeys` purpose `GOAL_NUDGE`, `GOAL_RESUME` (ready prompt, empty input line, goal-bound pane)", []string{"SendKeys"}, []string{"GOAL_NUDGE", "GOAL_RESUME"}, [5]string{no, "L", "L", "L, F", no}},
	{"`SendKeys` purpose `PROMPT_ANSWER` (`answering_prompt`)", []string{"SendKeys"}, []string{"PROMPT_ANSWER"}, [5]string{no, "L", "L", no, no}},
	{"`Interrupt`", []string{"Interrupt"}, nil, [5]string{no, "A", "A", "A", no}},
	{"`RespawnPane`", []string{"RespawnPane"}, nil, [5]string{no, "L", "L", "L, F", no}},
	{"`KillWindow` on a fleet window", []string{"KillWindow"}, []string{"fleet_window"}, [5]string{no, "L", "L", "L, F (after `merged_grace`)", no}},
	{"`KillWindow` on a developer's window", []string{"KillWindow"}, []string{"developer_window"}, [5]string{no, no, "L", no, no}},
	{"`CapturePane`", []string{"CapturePane"}, nil, [5]string{"A", "A", "A", "A", "A"}},
	{"`SetPaneAvailability`", []string{"SetPaneAvailability"}, nil, [5]string{no, "A", "A", "A", no}},
	{"`RegisterGoal`, `ReleaseGoal`", []string{"RegisterGoal", "ReleaseGoal"}, nil, [5]string{no, no, "A", "A", no}},
	{"`RunHarnessCommand` class OBSERVE", []string{"RunHarnessCommand"}, []string{"OBSERVE"}, [5]string{"A", "A", "A", "A", "A"}},
	{"`RunHarnessCommand` class SESSION", []string{"RunHarnessCommand"}, []string{"SESSION"}, [5]string{no, "A", "A", no, no}},
	{"`RunHarnessCommand` class GOAL (`/goal pause\\|resume\\|edit\\|clear`)", []string{"RunHarnessCommand"}, []string{"GOAL"}, [5]string{no, "A", "A", "A", no}},
	{"`RunHarnessCommand` class REVIEW", []string{"RunHarnessCommand"}, []string{"REVIEW"}, [5]string{no, "A", "A", no, no}},
	{"`RunHarnessCommand` class DESTRUCTIVE", []string{"RunHarnessCommand"}, []string{"DESTRUCTIVE"}, [5]string{no, "L!", "A", no, no}},
	{"`RunHarnessCommand` class DENY (and anything not in the catalog)", []string{"RunHarnessCommand"}, []string{"DENY"}, [5]string{no, no, no, no, no}},
	{"`PruneWorktree`", []string{"PruneWorktree"}, []string{"", "allow_dirty"}, [5]string{no, no, "A", "A (never `allow_dirty`)", no}},
	{"`EnsureGoalHooks`", []string{"EnsureGoalHooks"}, nil, [5]string{no, no, "A", "A", no}},
	{"`OpenAttach`, `CloseAttach`", []string{"OpenAttach", "CloseAttach"}, []string{"", "read_only"}, [5]string{"A (read-only)", "A", "A", no, no}},
	{"`SetLogCapture`", []string{"SetLogCapture"}, nil, [5]string{no, "A", "A", "A", no}},
	{"`SetLogShipping`", []string{"SetLogShipping"}, nil, [5]string{no, no, "A", "A", no}},
	{"`Drain`, `Undrain`", []string{"Drain", "Undrain"}, nil, [5]string{no, no, "A", no, no}},
}

// cell is one parsed matrix cell.
type cell struct {
	allowed       bool
	local         bool // L: waits when a local approval rule covers the kind
	alwaysLocal   bool // L!: always waits
	fleetOnly     bool // F
	fleetOrMarked bool // F*
	readOnly      bool // "(read-only)": only a read-only attach
	noAllowDirty  bool // "(never `allow_dirty`)"
}

func parseCell(s string) cell {
	note := ""
	if i := strings.Index(s, " ("); i >= 0 {
		s, note = s[:i], s[i+2:]
	}
	var c cell
	for _, tok := range strings.Split(s, ", ") {
		switch tok {
		case "A":
			c.allowed = true
		case "L":
			c.allowed, c.local = true, true
		case "L!":
			c.allowed, c.alwaysLocal = true, true
		case "F":
			c.fleetOnly = true
		case "F*":
			c.fleetOrMarked = true
		}
	}
	c.readOnly = strings.HasPrefix(note, "read-only")
	c.noAllowDirty = strings.HasPrefix(note, "never `allow_dirty`")
	return c
}

// column is the matrix column of a role. The proto has no ARBITER role
// yet, so that column is never reached; an unspecified or unknown role has
// no column and is refused.
func column(r typev1.Role) (int, bool) {
	switch r {
	case typev1.Role_ROLE_VIEWER:
		return 0, true
	case typev1.Role_ROLE_OPERATOR:
		return 1, true
	case typev1.Role_ROLE_OWNER:
		return 2, true
	case typev1.Role_ROLE_SCHEDULER:
		return 3, true
	default:
		return 0, false
	}
}

// variant is the matrix row a command's own fields select, or the
// handler's answer for KillWindow.
func variant(c *agentv1.Command, h Handler) string {
	switch k := c.GetKind().(type) {
	case *agentv1.Command_SendKeys:
		return strings.TrimPrefix(k.SendKeys.GetPurpose().String(), "SEND_KEYS_PURPOSE_")
	case *agentv1.Command_RunHarnessCommand:
		return strings.TrimPrefix(k.RunHarnessCommand.GetExpectedClass().String(), "COMMAND_CLASS_")
	case *agentv1.Command_StartGoal:
		if k.StartGoal.GetSpec().GetFreshContext() {
			return "fresh_context"
		}
	case *agentv1.Command_PruneWorktree:
		if k.PruneWorktree.GetAllowDirty() {
			return "allow_dirty"
		}
	case *agentv1.Command_OpenAttach:
		if k.OpenAttach.GetReadOnly() {
			return "read_only"
		}
	case *agentv1.Command_KillWindow:
		if v, ok := h.(Variant); ok && v.Variant(c) == "fleet_window" {
			return "fleet_window"
		}
		return "developer_window"
	}
	return ""
}

// roleCell is the cell for c's kind and variant and the requester's role;
// ok is false when the matrix refuses it (ROLE_INSUFFICIENT).
func roleCell(c *agentv1.Command, kind string, h Handler) (cell, bool) {
	col, ok := column(c.GetRequestedBy().GetRole())
	if !ok {
		return cell{}, false
	}
	v := variant(c, h)
	for _, row := range roleMatrix {
		if !contains(row.kinds, kind) || !matchVariant(row.variants, v) {
			continue
		}
		cl := parseCell(row.cells[col])
		switch {
		case !cl.allowed:
			return cl, false
		case cl.noAllowDirty && v == "allow_dirty":
			return cl, false
		case cl.readOnly && v != "read_only":
			return cl, false
		}
		return cl, true
	}
	return cell{}, false
}

func matchVariant(vs []string, v string) bool {
	if vs == nil {
		return v == ""
	}
	return contains(vs, v)
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// observeOnly reports whether the matrix lets a VIEWER ask for c: the one
// definition of an observe command, the only kind policy safe mode lets
// run (03e "observe only"; 02l POLICY_SAFE_MODE).
func observeOnly(c *agentv1.Command, kind string, h Handler) bool {
	v := variant(c, h)
	for _, row := range roleMatrix {
		if contains(row.kinds, kind) && matchVariant(row.variants, v) {
			cl := parseCell(row.cells[0])
			return cl.allowed && (!cl.readOnly || v == "read_only")
		}
	}
	return false
}
