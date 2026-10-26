package goals

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// ChangeState is what the code-host mirror says about the goal's change
// request (03l "Verification" joins the agent goal line with the provider
// mirror; the fingerprint includes both so re-evaluation is idempotent).
type ChangeState int

const (
	// ChangeUnknown is no change request found (yet): verification holds.
	ChangeUnknown ChangeState = iota
	// ChangeOpen is an open change request.
	ChangeOpen
	// ChangeMerged is a merged change request, or the work item closed as
	// done (both free the worker).
	ChangeMerged
	// ChangeClosedUnmerged is a change request closed without a merge.
	ChangeClosedUnmerged
)

// VerifyInput is everything DecideVerify reads: the mirror state, the
// goal's consent, and whether the verify grace ran out. Nothing here
// touches the network: the caller reads the already-synced mirror and the
// goal row, then writes the decision in one transaction.
type VerifyInput struct {
	// Change is the mirror state of the goal's change request.
	Change ChangeState
	// HeadMismatch is a found change request whose head differs from the
	// agent's HEAD: not a disagreement by itself (the driver may have
	// rebased), so verification uses provider merge state only.
	HeadMismatch bool
	// WorkItemClosedDone is the work item closed as done (routine runs
	// verify against the done rule snapshot instead; 03i).
	WorkItemClosedDone bool
	// WorkItemClosed is the item closed without being done.
	WorkItemClosed bool
	// MergeConsent is ADR 0008 merge consent on the goal.
	MergeConsent bool
	// ApprovedAndGreen is review APPROVED with checks SUCCESS for
	// head_commit (03j), the done state without merge consent.
	ApprovedAndGreen bool
	// PastGrace is verify_deadline passed (default 10 min, covers sync
	// lag).
	PastGrace bool
	// IntegrationDegraded holds VERIFYING past verify_grace: the
	// disagreement is unknowable from a stale mirror.
	IntegrationDegraded bool
}

// VerifyOutcome is what the verifier concludes (03l "Verification").
type VerifyOutcome int

const (
	// VerifyHold stays VERIFYING: nothing is concluded yet.
	VerifyHold VerifyOutcome = iota + 1
	// VerifyMerged moves to MERGED and frees the worker
	// (SetPaneAvailability, ReleaseGoal).
	VerifyMerged
	// VerifyDisagree moves to DISAGREE with a done_disagreement triage
	// item; the worker stays held.
	VerifyDisagree
	// VerifyCancelled moves to CANCELLED (the provider closed the work);
	// frees the worker with no success metric.
	VerifyCancelled
	// VerifyReadyForHumanMerge stays VERIFYING and raises
	// ready_for_human_merge: the goal is approved and green but the
	// consent has no merge, so the owner's merge is still required.
	VerifyReadyForHumanMerge
)

// DecideVerify checks one DONE claim against the code host (03l
// "Verification"). Merged (or the work item closed as done) frees the
// worker; open past verify_grace disagrees; closed unmerged cancels when
// the item is closed and disagrees while it is open. Without merge
// consent the done state is approved-and-green and waits for a person.
// A DEGRADED integration holds past grace: nothing is concluded from a
// stale mirror.
func DecideVerify(in VerifyInput) VerifyOutcome {
	if in.Change == ChangeMerged || in.WorkItemClosedDone {
		return VerifyMerged
	}
	if in.Change == ChangeClosedUnmerged {
		if in.WorkItemClosed {
			return VerifyCancelled
		}
		return VerifyDisagree
	}
	if in.IntegrationDegraded {
		return VerifyHold
	}
	if !in.MergeConsent {
		if in.ApprovedAndGreen {
			return VerifyReadyForHumanMerge
		}
		return VerifyHold
	}
	if in.PastGrace {
		return VerifyDisagree
	}
	return VerifyHold
}

// WorkStageAfter maps a goal's terminal phase to the work item stage that
// follows it (03h "Work item stage after a goal ends"). Live phases keep
// the item on its goal: STARTING plans it (DISPATCHED), VERIFYING claims
// done (DONE_CLAIMED), DISAGREE marks the mismatch, the rest run
// (RUNNING). Anything the table does not name leaves the stage alone.
func WorkStageAfter(phase hubv1.GoalPhase) (hubv1.WorkStage, hubv1.WorkManualReason) {
	none := hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED
	switch phase {
	case hubv1.GoalPhase_GOAL_PHASE_MERGED,
		hubv1.GoalPhase_GOAL_PHASE_CLOSED,
		hubv1.GoalPhase_GOAL_PHASE_CANCELLED:
		return hubv1.WorkStage_WORK_STAGE_CLOSED, none
	case hubv1.GoalPhase_GOAL_PHASE_START_FAILED,
		hubv1.GoalPhase_GOAL_PHASE_EXPIRED,
		hubv1.GoalPhase_GOAL_PHASE_ABANDONED:
		return hubv1.WorkStage_WORK_STAGE_READY, none
	case hubv1.GoalPhase_GOAL_PHASE_START_EXHAUSTED:
		return hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY, hubv1.WorkManualReason_WORK_MANUAL_REASON_START_EXHAUSTED
	case hubv1.GoalPhase_GOAL_PHASE_CLEARED:
		return hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY, hubv1.WorkManualReason_WORK_MANUAL_REASON_GOAL_CLEARED
	case hubv1.GoalPhase_GOAL_PHASE_STARTING:
		return hubv1.WorkStage_WORK_STAGE_DISPATCHED, none
	case hubv1.GoalPhase_GOAL_PHASE_VERIFYING:
		return hubv1.WorkStage_WORK_STAGE_DONE_CLAIMED, none
	case hubv1.GoalPhase_GOAL_PHASE_DISAGREE:
		return hubv1.WorkStage_WORK_STAGE_DISAGREE, none
	case hubv1.GoalPhase_GOAL_PHASE_START_UNKNOWN,
		hubv1.GoalPhase_GOAL_PHASE_RUNNING,
		hubv1.GoalPhase_GOAL_PHASE_WAITING,
		hubv1.GoalPhase_GOAL_PHASE_BLOCKED_OWNER,
		hubv1.GoalPhase_GOAL_PHASE_STUCK,
		hubv1.GoalPhase_GOAL_PHASE_STOPPED,
		hubv1.GoalPhase_GOAL_PHASE_COOLDOWN,
		hubv1.GoalPhase_GOAL_PHASE_PAUSED,
		hubv1.GoalPhase_GOAL_PHASE_LIMIT_WAIT,
		hubv1.GoalPhase_GOAL_PHASE_PANE_LOST:
		return hubv1.WorkStage_WORK_STAGE_RUNNING, none
	default:
		return hubv1.WorkStage_WORK_STAGE_UNSPECIFIED, none
	}
}

// Default goal text for repos without eshu's hooks (0.18).
//
// Approved wording: issue #63 closed 2026-10-26 with the owner direction
// to keep it generic (no eshu-specific paths or assumptions), matching
// 03c "Default goal text". stub=false always; the row-22 gate is lifted.
const defaultGoalTextTemplate = `Work issue %s on branch %s: <one-line summary>.
Your goal file is .fleet-board/goal-<goal-id> at the top of this checkout.
Keep its first line current at all times:
- while working, the first line is this objective in one line;
- waiting on outside work you started (CI, a command still running):
  BLOCKED: <reason> WATCH=<pid> with the live watcher pid;
- waiting on a person: BLOCKED: <reason> OWNER=<who> ACTION=<what>;
- anything else that stops you: BLOCKED: <reason>, and wait;
- done: DONE, and nothing else, only when the work is merged
  (or, without merge consent, when the PR is approved and its checks are green).
Rules:
1. First, claim the issue so no second fleet takes it:
   gh issue comment <n> --body "fleet <goal-id>: working this issue".
   If someone else already claimed it, stop: write
   BLOCKED: issue already claimed OWNER=fleet ACTION=decide and wait.
2. Push the branch, open the PR, link the issue (Fixes #n only when the
   merge should close it). Never force-push someone else's commits.
3. After review asks for changes, address them and push again. After three
   rounds of CHANGES_REQUESTED with no merge, stop: write
   BLOCKED: review loop OWNER=fleet ACTION=decide and wait for a person.
   Do not push new fixes past that point; the hub pauses the goal.`

// Fingerprint hashes the state a transition records (03l "Fingerprint"):
// h(line_state, first_line, watch_alive, head_commit, cr_state,
// cr_head_sha). Equal states hash equal, so the verifier's re-evaluation
// and the triage dedupe stay idempotent; any change moves the hash, so a
// new state is never confused with the old one.
func Fingerprint(line typev1.GoalLineState, firstLine string, watchAlive bool, headCommit, crState, crHead string) string {
	alive := "0"
	if watchAlive {
		alive = "1"
	}
	parts := []string{line.String(), firstLine, alive, headCommit, crState, crHead}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

// DefaultGoalText builds the objective text for a repo whose checkout
// does not ship eshu's hooks. The wording is the #63-approved 0.18 text
// (03c "Default goal text"); stub is always false.
func DefaultGoalText(repo string, issue uint64, branch string) (text string, stub bool) {
	return fmt.Sprintf(defaultGoalTextTemplate, fmt.Sprintf("%s#%d", repo, issue), branch), false
}
