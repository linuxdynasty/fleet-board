package ingest

import (
	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Caps on the Event free text the hub scrubs (02e "Size limits").
const (
	maxParseMessage = 1024
	maxProbeMessage = 256
	maxGoalLine     = 4096
	maxGoalToken    = 256
)

// CleanEvent makes ev's free text safe, in place, on the stream's ingest
// goroutine before the writer job (never in the transaction): the Event
// fields the agent fills with its own words (row 5 ruling, fork 9:
// UsageReading.parse_message, cap 1 KiB, and ProbeHealth.message, 256
// bytes; row 9: the GoalState lines) lose their control characters, are
// cut to their cap and only then go to the shared scrubber, so a hostile
// long string costs what a short one does (04b). The other strings of an
// Event are the rows that apply its kind to clean (rows 7 to 10), as for
// [Clean].
func CleanEvent(ev *agentv1.Event) Cleaned {
	var c Cleaned
	if u := ev.GetUsageReading(); u != nil {
		u.ParseMessage = c.scrubTo(u.GetParseMessage(), maxParseMessage)
	}
	if p := ev.GetProbeHealth(); p != nil {
		p.Message = c.scrubTo(p.GetMessage(), maxProbeMessage)
	}
	if g := ev.GetGoalState(); g != nil {
		cleanGoalState(&c, g)
	}
	return c
}

// cleanGoalState cuts one GoalState's text to its caps (row 9): the two
// long lines are scrubbed like any agent-written free text, the rest only
// loses controls and overlong bytes.
func cleanGoalState(c *Cleaned, g *agentv1.GoalState) {
	g.FirstLine = c.scrubTo(g.GetFirstLine(), maxGoalLine)
	g.ConsentLine = c.scrubTo(g.GetConsentLine(), maxGoalLine)
	g.Owner = c.text(g.GetOwner(), maxGoalToken)
	g.Action = c.text(g.GetAction(), maxGoalToken)
	g.HeadCommit = c.text(g.GetHeadCommit(), maxGoalToken)
	g.Branch = c.text(g.GetBranch(), maxGoalToken)
	g.GoalFile = c.text(g.GetGoalFile(), maxPath)
	g.WorktreePath = c.text(g.GetWorktreePath(), maxPath)
}
