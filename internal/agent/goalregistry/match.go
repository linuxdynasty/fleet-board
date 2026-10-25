package goalregistry

import (
	"context"
	"path/filepath"
	"strings"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

// Call is one hook invocation after parsing (lane 2 owns the payload
// and environment parsing; this package owns the registry decision).
// FLEET_GOAL_ID, when present, is only a fast path for a window the
// fleet just created: it is never trusted over the registry, so it is
// not even an input here.
type Call struct {
	// Harness is the adapter name: claude, codex, muse, cursor, opencode.
	Harness string
	Kind    agentv1.HookEventKind
	// Pane comes from TMUX_PANE for harnesses whose pane environment
	// reaches hooks (Claude Code, Codex, Cursor); nil for Muse, whose
	// plugin hooks see no pane variables (01d).
	Pane *typev1.PaneKey
	// Session is the harness session id from the payload (untrusted).
	Session string
	// Cwd is the payload's working directory (Muse matching).
	Cwd string
	// Raw is the harness's own event name, for unmapped kinds
	// (untrusted, capped at emit).
	Raw string
}

// Outcome is what a hook does: stay silent (exit 0, no output, the
// developer's own session or an unmatched pane) or report Entry's goal.
// LearnedSession says this call taught the entry its harness session id
// (E9): the caller then emits it with EmitLearned.
type Outcome struct {
	Silent         bool
	Entry          *agentv1.GoalRegistration
	LearnedSession bool
}

// Resolve matches one hook call to its registry entry (01d, 02h "Goal
// registry"): SessionStart is always inert; a harness session id that
// matches an entry is that goal; else the pane (Claude Code, Codex and
// Cursor); else, for Muse only, the cwd at or under an entry's worktree
// while its session is still unlearned and exactly one Muse pane is
// registered for that checkout (REPO_DRIVER entries never match by cwd:
// the developer's other sessions run there). Anything else is silent.
func (r *Registry) Resolve(call Call) Outcome {
	if call.Kind == agentv1.HookEventKind_HOOK_EVENT_KIND_SESSION_START {
		return Outcome{Silent: true}
	}
	session := goalfile.Clean(call.Session, 256)
	if session != "" {
		if entry, ok := r.BySession(session); ok {
			return Outcome{Entry: entry}
		}
	}
	harness := goalfile.Clean(call.Harness, 32)
	if harness != "muse" && call.Pane != nil {
		if entry, ok := r.ByPane(call.Pane); ok {
			return r.learn(entry, session)
		}
		return Outcome{Silent: true}
	}
	if harness == "muse" {
		if entry, ok := r.matchCwd(call.Cwd); ok {
			return r.learn(entry, session)
		}
	}
	return Outcome{Silent: true}
}

// learn records a fresh session id on a matched entry (the first
// UserPromptSubmit or PreToolUse after registration, E9) and reports
// whether it learned anything.
func (r *Registry) learn(entry *agentv1.GoalRegistration, session string) Outcome {
	if session == "" || entry.GetHarnessSessionId() != "" {
		return Outcome{Entry: entry}
	}
	changed, err := r.LearnSession(entry.GetPane(), session)
	if err != nil || !changed {
		return Outcome{Entry: entry}
	}
	return Outcome{Entry: entry, LearnedSession: true}
}

// matchCwd finds the one unlearned non-driver Muse entry whose worktree
// holds cwd, only when exactly one Muse pane is registered for that
// checkout. Two panes, or a cwd under two checkouts, never guesses.
func (r *Registry) matchCwd(cwd string) (*agentv1.GoalRegistration, bool) {
	cwd = filepath.Clean(cwd)
	if cwd == "" || cwd == "." {
		return nil, false
	}
	trees := map[string][]*agentv1.GoalRegistration{}
	for _, entry := range r.Entries() {
		if entry.GetHarness().GetName() != "muse" || entry.GetHarnessSessionId() != "" || entry.GetRepoDriver() {
			continue
		}
		wt := filepath.Clean(entry.GetWorktreePath())
		if wt == "" || wt == "." {
			continue
		}
		if cwd != wt && !strings.HasPrefix(cwd, wt+string(filepath.Separator)) {
			continue
		}
		trees[wt] = append(trees[wt], entry)
	}
	if len(trees) != 1 {
		return nil, false
	}
	for _, entries := range trees {
		if len(entries) != 1 {
			return nil, false
		}
		return entries[0], true
	}
	return nil, false
}

// EmitLearned reports a just-learned harness session id (E9,
// pane_harness_session, NEVER_DROP): the pane, the id, and the goal the
// registry binds to it. The transcript path stays empty: K3 keys Muse
// transcripts by session id (01d), and other harnesses' paths are row
// 25's to fill.
func (r *Registry) EmitLearned(ctx context.Context, entry *agentv1.GoalRegistration) error {
	if r.emit == nil {
		return nil
	}
	_, err := r.emit(ctx, &agentv1.Event{Body: &agentv1.Event_PaneHarnessSession{
		PaneHarnessSession: &agentv1.PaneHarnessSession{
			Pane: entry.GetPane(), Harness: entry.GetHarness(),
			HarnessSessionId: entry.GetHarnessSessionId(),
			Goal:             entry.GetGoal(),
			Binding:          agentv1.GoalBindingSource_GOAL_BINDING_SOURCE_REGISTRY,
		},
	}})
	return err
}
