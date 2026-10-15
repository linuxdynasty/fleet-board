package doctor

import (
	"context"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/agent/probe"
)

// probeTrustHarness is one harness with a folder-trust screen. OpenCode has
// none, so it is never checked here (01 C3).
type probeTrustHarness struct {
	name    string
	display string
	step    string
}

// probeTrustSteps is the one-time setup per harness (01 C3): the owner
// answers the trust question by hand, once per harness, in a terminal of
// their own. <dir> is the probe folder in full.
func probeTrustSteps(dir string) []probeTrustHarness {
	cd := "cd " + shellQuote(dir) + " && "
	return []probeTrustHarness{
		{"claude", "Claude Code", cd + "claude. On 'Accessing workspace' press Down, choose 'Yes, I trust this folder', Enter. Then type /exit."},
		{"codex", "Codex", cd + "codex. Skip any update (Esc, never 'Update now'); on a background-server question choose '1. Run without daemon this time'; on 'Folder access' choose '1. Trust and continue'. Then type /exit."},
		{"muse", "Muse", cd + "muse. On 'Do you trust this workspace?' choose '1 Trust and continue'. Then leave Muse the way you usually do."},
		{"cursor", "Cursor Agent", cd + "cursor-agent. On 'Workspace Trust Required' choose '[a] Trust this workspace'. Then leave Cursor Agent the way you usually do."},
	}
}

// probeTrustCheck is the probe folder trust per harness (01 C3): the trust
// screen comes from the adapters' own verified ParseReady, read through
// [probe.Classify], the same detection the probe pass uses. The capture
// starts the harness in the probe folder, reads the first screen, sends no
// key and tears the session down; nothing is ever typed into a trust
// screen. A harness is named waiting only when its own trust screen was
// seen; otherwise the check says it was not checked.
func probeTrustCheck(ctx context.Context, e *Env) Outcome {
	dir := e.opts.ProbeDir
	if dir == "" {
		return unknown("no probe folder is configured", "report this as a bug in fleet-agent doctor")
	}
	capture := e.opts.ProbeTrustCapture
	if capture == nil {
		return unknown("the probe folder trust was not checked on this run", "report this as a bug in fleet-agent doctor")
	}
	var waiting []probeTrustHarness
	read := 0
	for _, h := range probeTrustSteps(dir) {
		screen, installed, err := capture(ctx, h.name, dir)
		if ctx.Err() != nil {
			return unknown("doctor was stopped before the trust screens were read", "run fleet-agent doctor again")
		}
		if err != nil || !installed {
			continue
		}
		read++
		if probe.Classify(h.name, screen).Outcome == probe.OutcomeTrust {
			waiting = append(waiting, h)
		}
	}
	if len(waiting) > 0 {
		names := make([]string, 0, len(waiting))
		steps := make([]string, 0, len(waiting))
		for _, h := range waiting {
			names = append(names, h.display)
			steps = append(steps, h.display+": "+h.step)
		}
		return fail(joinNames(names)+" waiting for you to trust the probe folder",
			"In a terminal of your own, once per harness: "+strings.Join(steps, " "))
	}
	if read == 0 {
		return unknown("the probe folder trust was not checked: no harness screen could be read", "check that a harness is installed and run fleet-agent doctor again")
	}
	return ok("no harness is waiting for you to trust the probe folder")
}

// joinNames joins display names in plain words: "A", "A and B",
// "A, B and C".
func joinNames(names []string) string {
	switch len(names) {
	case 1:
		return names[0] + " is"
	case 2:
		return names[0] + " and " + names[1] + " are"
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " are"
	}
}
