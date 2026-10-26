package goalregistry

import (
	"context"
	"syscall"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

// TurnEnd reports a goal turn's end (E8, harness_hook NEVER_DROP).
// Silent calls emit nothing. A matched USER_PROMPT only learns the
// session (E9); TURN_END, SESSION_STOP and INTERRUPT of a goal pane
// also emit HarnessHookEvent. For TURN_END and SESSION_STOP the event
// carries the Stop guard's verdict (01d): DONE allows, a BLOCKED line
// with a live WATCH pid or with OWNER and ACTION allows, any other
// BLOCKED or working line refuses, and a missing or unreadable goal
// file allows (stuck detection owns the vanished file, not the guard).
// INTERRUPT carries no verdict: the guard answers stops, not pauses.
// files reads the goal file; alive probes a WATCH pid (AlivePID).
func (r *Registry) TurnEnd(ctx context.Context, call Call, files goalfile.Writer, alive func(uint32) bool) (Outcome, error) {
	out := r.Resolve(call)
	if out.Silent {
		return out, nil
	}
	if out.LearnedSession {
		if err := r.EmitLearned(ctx, out.Entry); err != nil {
			return out, err
		}
	}
	switch call.Kind {
	case agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END,
		agentv1.HookEventKind_HOOK_EVENT_KIND_SESSION_STOP,
		agentv1.HookEventKind_HOOK_EVENT_KIND_INTERRUPT:
	default:
		return out, nil
	}
	decision := agentv1.HookDecision_HOOK_DECISION_UNSPECIFIED
	if call.Kind != agentv1.HookEventKind_HOOK_EVENT_KIND_INTERRUPT {
		decision = guard(out.Entry, files, alive)
	}
	if r.emit == nil {
		return out, nil
	}
	_, err := r.emit(ctx, &agentv1.Event{Body: &agentv1.Event_HarnessHook{
		HarnessHook: &agentv1.HarnessHookEvent{
			Pane: out.Entry.GetPane(), Harness: out.Entry.GetHarness(), Kind: call.Kind,
			HarnessSessionId: out.Entry.GetHarnessSessionId(),
			RawEventName:     goalfile.Clean(call.Raw, 128),
			GoalId:           out.Entry.GetGoal(), Decision: decision,
		},
	}})
	return out, err
}

// guard answers the Stop guard from the goal file's first line. It
// allows when the file is gone or unreadable: with no evidence of an
// unfinished goal the turn ends, and G2 plus stuck detection own the
// missing file.
func guard(entry *agentv1.GoalRegistration, files goalfile.Writer, alive func(uint32) bool) agentv1.HookDecision {
	allow := agentv1.HookDecision_HOOK_DECISION_ALLOW
	block := agentv1.HookDecision_HOOK_DECISION_BLOCK
	got, err := files.ReadGoalFile(entry.GetWorktreePath(), entry.GetGoal().GetValue())
	if err != nil {
		return allow
	}
	switch got.State {
	case typev1.GoalLineState_GOAL_LINE_STATE_DONE:
		return allow
	case typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH:
		if alive != nil && alive(got.WatchPID) {
			return allow
		}
		return block
	case typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_OWNER:
		return allow
	default:
		return block
	}
}

// AlivePID reports whether pid names a live process, for WATCH= checks.
// A pid the caller may not signal (EPERM) is alive; pid 0 never is.
func AlivePID(pid uint32) bool {
	if pid == 0 || uint64(pid) > uint64(^uint32(0)) {
		return false
	}
	err := syscall.Kill(int(pid), 0)
	return err == nil || err == syscall.EPERM
}
