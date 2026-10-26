package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/ulid"
)

// controlInventory is what ControlGoal reads about the goal's harness:
// the snapshot-reported goal-control account and the catalog hash
// RunHarnessCommand must match.
type controlInventory struct {
	control     *typev1.GoalControl
	catalogHash string
}

// controlInventoryFor reads the machine's reported harness (row 22's
// machine_harness replace). A machine that never reported the harness
// has no account on file: GOAL_SUBCOMMAND_UNSUPPORTED before anything is
// sent (02m "before sending when the hub knows").
func controlInventoryFor(ctx context.Context, tx *sql.Tx, machine, harness string) (controlInventory, error) {
	inv, found, err := statedb.ReadMachineHarnessTx(ctx, tx, machine, harness)
	if err != nil {
		return controlInventory{}, fmt.Errorf("dispatch: read harness inventory: %w", err)
	}
	if !found {
		return controlInventory{}, apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_GOAL_SUBCOMMAND_UNSUPPORTED,
			"the machine never reported this harness", map[string]string{"harness": harness}, nil)
	}
	var gc typev1.GoalControl
	if err := protojson.Unmarshal([]byte(inv.GoalControlJSON), &gc); err != nil {
		return controlInventory{}, fmt.Errorf("dispatch: read goal control: %w", err)
	}
	return controlInventory{control: &gc, catalogHash: inv.CatalogHash}, nil
}

// applyHubControl commits a hub-only move: MARK_NOT_STARTED abandons the
// goal and frees the item; RECHECK runs the verifier now.
func (f *Fleet) applyHubControl(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	m *hubv1.ControlGoalRequest, ctl *controlGoal, now time.Time,
) (statedb.GoalRow, error) {
	nowMs := now.UnixMilli()
	var after statedb.GoalRow
	var err error
	switch m.GetAction() {
	case hubv1.GoalAction_GOAL_ACTION_MARK_NOT_STARTED:
		after, err = f.moveControlGoal(ctx, tx, ctl, statedb.GoalTransition{
			To: "GOAL_PHASE_ABANDONED", Reason: "GOAL_PHASE_REASON_OPERATOR_NOT_STARTED",
			Actor: who.GetId().GetValue(), CommandID: "", NowMs: nowMs,
		}, actorOf(who), controlAuditAction, nowMs)
		if err != nil {
			return statedb.GoalRow{}, err
		}
		if err := f.stageControlItem(ctx, tx, ctl, after); err != nil {
			return statedb.GoalRow{}, err
		}
	case hubv1.GoalAction_GOAL_ACTION_RECHECK:
		after, err = f.verifyGoalTx(ctx, tx, who, ctl, nowMs)
		if err != nil {
			return statedb.GoalRow{}, err
		}
		if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
			Actor: actorOf(who), Action: controlAuditAction, Target: "goals/" + ctl.row.ID,
			RequestID: m.GetRequestId(), Now: time.UnixMilli(nowMs),
		}); err != nil {
			return statedb.GoalRow{}, err
		}
	default:
		return statedb.GoalRow{}, fmt.Errorf("dispatch: hub control for %v", m.GetAction())
	}
	if err := logRequest(ctx, tx, who.GetId().GetValue(), m.GetRequestId(), "", controlHash(m), time.UnixMilli(nowMs)); err != nil {
		return statedb.GoalRow{}, err
	}
	return after, nil
}

// moveControlGoal records one control move with its audit, Watch count
// and stage follow-up.
func (f *Fleet) moveControlGoal(ctx context.Context, tx *sql.Tx, ctl *controlGoal,
	tr statedb.GoalTransition, actor statedb.Actor, auditAction string, nowMs int64,
) (statedb.GoalRow, error) {
	status := cloneGoalStatus(ctl.status)
	applyStatusMove(status, tr)
	raw, err := protojson.Marshal(status)
	if err != nil {
		return statedb.GoalRow{}, fmt.Errorf("dispatch: encode control status: %w", err)
	}
	tr.Status = string(raw)
	tr.Fingerprint = transitionFingerprint(ctl, tr)
	st, err := statedb.NextStamp(ctx, tx, time.UnixMilli(nowMs))
	if err != nil {
		return statedb.GoalRow{}, err
	}
	tr.HubSeq = st.HubSeq
	after, err := statedb.ApplyGoalTransition(ctx, tx, ctl.row.ID, tr)
	if err != nil {
		return statedb.GoalRow{}, fmt.Errorf("dispatch: control move: %w", err)
	}
	if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
		Actor: actor, Action: auditAction, Target: "goals/" + ctl.row.ID,
		CommandID: tr.CommandID, Now: time.UnixMilli(nowMs),
	}); err != nil {
		return statedb.GoalRow{}, err
	}
	f.countGoalMove(ctx, ctl.row.Phase, tr.To, tr.Reason)
	return after, nil
}

// verifierActor is the audit actor of the verifier's own moves.
func verifierActor() statedb.Actor {
	return statedb.Actor{ID: "verifier", Kind: "HUB"}
}

// stageControlItem follows a control move on the work item. A move that
// leaves the phase writes no stage: the item already shows it (a pause,
// a resume, a same-phase re-arm), so the version and Watch stay quiet.
func (f *Fleet) stageControlItem(ctx context.Context, tx *sql.Tx, ctl *controlGoal, after statedb.GoalRow) error {
	if after.Phase == ctl.row.Phase {
		return nil
	}
	var spec hubv1.GoalDefinition
	if err := protojson.Unmarshal([]byte(after.Spec), &spec); err != nil {
		return fmt.Errorf("dispatch: read moved goal spec: %w", err)
	}
	key := statedb.SyncKey(spec.GetWorkItem().GetKey().GetIntegration().GetValue(),
		workItemKindName(spec.GetWorkItem().GetKey().GetKind()),
		spec.GetWorkItem().GetKey().GetProject(), spec.GetWorkItem().GetKey().GetKey())
	phase, err := hubPhaseOf(after.Phase)
	if err != nil {
		return err
	}
	stage, reason := goals.WorkStageAfter(phase)
	return statedb.SetWorkItemGoalStage(ctx, tx, key, int32(stage), int32(reason), after.HubSeq)
}

// applyStatusMove stamps the phase move onto the status the transition
// carries.
func applyStatusMove(status *hubv1.GoalStatus, tr statedb.GoalTransition) {
	phase, err := hubPhaseOf(tr.To)
	if err == nil {
		status.Phase = phase
		status.PhaseTime = timestamppb.New(time.UnixMilli(tr.NowMs))
	}
	if r, err := hubPhaseReasonOf(tr.Reason); err == nil {
		status.PhaseReason = r
	}
}

// transitionFingerprint hashes the state the transition records.
func transitionFingerprint(ctl *controlGoal, tr statedb.GoalTransition) string {
	return goals.Fingerprint(ctl.status.GetLineState(), ctl.status.GetFirstLine(),
		false, ctl.status.GetHeadCommit(), tr.To, "")
}

// hubPhaseOf parses a stored phase name into the contract enum.
func hubPhaseOf(name string) (hubv1.GoalPhase, error) {
	v, ok := hubv1.GoalPhase_value[name]
	if !ok {
		return hubv1.GoalPhase_GOAL_PHASE_UNSPECIFIED, fmt.Errorf("dispatch: unknown phase %q", name)
	}
	return hubv1.GoalPhase(v), nil
}

// hubPhaseReasonOf parses a stored reason name into the contract enum.
func hubPhaseReasonOf(name string) (hubv1.GoalPhaseReason, error) {
	v, ok := hubv1.GoalPhaseReason_value[name]
	if !ok {
		return hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED, fmt.Errorf("dispatch: unknown reason %q", name)
	}
	return hubv1.GoalPhaseReason(v), nil
}

// workItemKindName renders a WorkItemKind for the canonical mirror id.
func workItemKindName(k typev1.WorkItemKind) string {
	s := k.String()
	for _, p := range []string{"WORK_ITEM_KIND_"} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

// controlHash is the idempotency hash of a control request body.
func controlHash(m *hubv1.ControlGoalRequest) []byte {
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil
	}
	sum := sha256.Sum256(body)
	return sum[:]
}

// verifyGoalTx runs the verifier now (ControlGoal RECHECK, 03l
// "Verification"): the DONE claim against the code-host mirror, decided
// by goals.DecideVerify and applied in this transaction. A hold writes
// nothing and returns the row; every move audits goal.state_change and
// follows the work item.
func (f *Fleet) verifyGoalTx(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	ctl *controlGoal, nowMs int64,
) (statedb.GoalRow, error) {
	in, err := f.verifyInputTx(ctx, tx, ctl, nowMs)
	if err != nil {
		return statedb.GoalRow{}, err
	}
	switch goals.DecideVerify(in) {
	case goals.VerifyHold:
		return ctl.row, nil
	case goals.VerifyMerged:
		return f.applyVerifyMove(ctx, tx, ctl, statedb.GoalTransition{
			To: "GOAL_PHASE_MERGED", Reason: "GOAL_PHASE_REASON_DONE_CLAIMED",
			Actor: "verifier", NowMs: nowMs,
		}, nowMs)
	case goals.VerifyDisagree:
		after, err := f.applyVerifyMove(ctx, tx, ctl, statedb.GoalTransition{
			To: "GOAL_PHASE_DISAGREE", Reason: "GOAL_PHASE_REASON_UNSPECIFIED",
			Actor: "verifier", NowMs: nowMs,
		}, nowMs)
		if err != nil {
			return statedb.GoalRow{}, err
		}
		if err := f.ensureDisagreeTriage(ctx, tx, ctl, after, nowMs); err != nil {
			return statedb.GoalRow{}, err
		}
		return after, nil
	case goals.VerifyCancelled:
		reason := "GOAL_PHASE_REASON_CHANGE_REQUEST_CLOSED"
		if in.Change == goals.ChangeUnknown {
			reason = "GOAL_PHASE_REASON_WORK_ITEM_CLOSED"
		}
		return f.applyVerifyMove(ctx, tx, ctl, statedb.GoalTransition{
			To: "GOAL_PHASE_CANCELLED", Reason: reason, Actor: "verifier", NowMs: nowMs,
		}, nowMs)
	default:
		if err := f.ensureHumanMergeTriage(ctx, tx, ctl, nowMs); err != nil {
			return statedb.GoalRow{}, err
		}
		return ctl.row, nil
	}
}

// applyVerifyMove records one verifier move with its audit and the work
// item follow-up.
func (f *Fleet) applyVerifyMove(ctx context.Context, tx *sql.Tx, ctl *controlGoal,
	tr statedb.GoalTransition, nowMs int64,
) (statedb.GoalRow, error) {
	after, err := f.moveControlGoal(ctx, tx, ctl, tr, verifierActor(), controlMoveAuditAction, nowMs)
	if err != nil {
		return statedb.GoalRow{}, err
	}
	if err := f.stageControlItem(ctx, tx, ctl, after); err != nil {
		return statedb.GoalRow{}, err
	}
	return after, nil
}

// verifyInputTx gathers the verifier's input from the goal and the
// code-host mirror: the change request the definition names, else the one
// working on the goal's branch, plus the goal's consent and verify
// deadline (03l "Verification joins the agent goal line with the provider
// mirror").
func (f *Fleet) verifyInputTx(ctx context.Context, tx *sql.Tx, ctl *controlGoal, nowMs int64) (goals.VerifyInput, error) {
	var in goals.VerifyInput
	spec := ctl.spec
	in.MergeConsent = hasConsent(spec.GetConsent(), typev1.ConsentAct_CONSENT_ACT_MERGE)
	if dl := ctl.status.GetVerifyDeadlineTime(); dl != nil {
		in.PastGrace = nowMs > dl.AsTime().UnixMilli()
	}
	crKey := spec.GetChangeRequest().GetKey()
	var raw string
	if crKey.GetKey() != "" {
		key := statedb.SyncKey(crKey.GetIntegration().GetValue(),
			workItemKindName(crKey.GetKind()), spec.GetRepo().GetPath(), crKey.GetKey())
		body, err := statedb.ReadChangeRequestJSONTx(ctx, tx, key)
		if err != nil {
			if !errors.Is(err, statedb.ErrChangeRequestUnknown) {
				return in, fmt.Errorf("dispatch: read change request: %w", err)
			}
		} else {
			raw = body
		}
	}
	if raw == "" {
		branch := ctl.status.GetBranch()
		key, found, err := statedb.FindChangeRequestByBranchTx(ctx, tx,
			spec.GetWorkItem().GetKey().GetIntegration().GetValue(),
			spec.GetWorkItem().GetKey().GetProject(), branch)
		if err != nil {
			return in, fmt.Errorf("dispatch: find change request: %w", err)
		}
		if found {
			body, err := statedb.ReadChangeRequestJSONTx(ctx, tx, key)
			if err != nil {
				return in, fmt.Errorf("dispatch: read change request: %w", err)
			}
			raw = body
		}
	}
	var itemClosed bool
	if len(ctl.row.WorkItem) > 0 {
		if st, err := statedb.GetWorkItemStateTx(ctx, tx, ctl.row.WorkItem); err == nil {
			itemClosed = hubv1.WorkStage(st.Stage) == hubv1.WorkStage_WORK_STAGE_CLOSED
		} else if !errors.Is(err, statedb.ErrWorkItemUnknown) {
			return in, fmt.Errorf("dispatch: read verify item: %w", err)
		}
	}
	if raw == "" {
		in.Change = goals.ChangeUnknown
		in.WorkItemClosedDone = itemClosed
		in.WorkItemClosed = itemClosed
		return in, nil
	}
	var cr integrationv1.ChangeRequest
	if err := protojson.Unmarshal([]byte(raw), &cr); err != nil {
		return in, fmt.Errorf("dispatch: read change request: %w", err)
	}
	switch cr.GetState() {
	case integrationv1.WorkItemState_WORK_ITEM_STATE_MERGED:
		in.Change = goals.ChangeMerged
	case integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN,
		integrationv1.WorkItemState_WORK_ITEM_STATE_IN_PROGRESS,
		integrationv1.WorkItemState_WORK_ITEM_STATE_DRAFT:
		in.Change = goals.ChangeOpen
	default:
		in.Change = goals.ChangeClosedUnmerged
	}
	in.HeadMismatch = cr.GetHeadCommit() != "" && cr.GetHeadCommit() != ctl.status.GetHeadCommit()
	in.WorkItemClosedDone = itemClosed && in.Change != goals.ChangeOpen && in.Change != goals.ChangeClosedUnmerged
	in.WorkItemClosed = itemClosed && in.Change == goals.ChangeClosedUnmerged
	in.ApprovedAndGreen = cr.GetReviewDecision() == integrationv1.ReviewDecision_REVIEW_DECISION_APPROVED &&
		cr.GetChecks().GetRollup() == integrationv1.CheckRollup_CHECK_ROLLUP_SUCCESS
	return in, nil
}

// ensureDisagreeTriage raises done_disagreement for a DISAGREE move,
// skipped when the state is already under review.
func (f *Fleet) ensureDisagreeTriage(ctx context.Context, tx *sql.Tx, ctl *controlGoal, after statedb.GoalRow, nowMs int64) error {
	id, err := ulid.New(time.UnixMilli(nowMs), f.random)
	if err != nil {
		return fmt.Errorf("dispatch: mint triage id: %w", err)
	}
	st, err := statedb.NextStamp(ctx, tx, time.UnixMilli(nowMs))
	if err != nil {
		return err
	}
	fp := goals.Fingerprint(ctl.status.GetLineState(), ctl.status.GetFirstLine(),
		false, ctl.status.GetHeadCommit(), "open", "")
	_, err = statedb.InsertGoalTriageTx(ctx, tx, statedb.GoalTriage{
		ID: id, Kind: "TRIAGE_KIND_DONE_DISAGREEMENT", Fingerprint: fp, GoalID: ctl.row.ID,
		Title:      "DONE disagreed by the code host on " + ctl.status.GetBranch(),
		DetailJSON: `{"work_item":"` + ctl.row.WorkItem + `"}`,
		MachineID:  ctl.row.MachineID,
	}, nowMs, st.HubSeq)
	return err
}

// ensureHumanMergeTriage raises ready_for_human_merge for an approved and
// green goal the consent leaves unmerged, skipped while one is open.
func (f *Fleet) ensureHumanMergeTriage(ctx context.Context, tx *sql.Tx, ctl *controlGoal, nowMs int64) error {
	id, err := ulid.New(time.UnixMilli(nowMs), f.random)
	if err != nil {
		return fmt.Errorf("dispatch: mint triage id: %w", err)
	}
	st, err := statedb.NextStamp(ctx, tx, time.UnixMilli(nowMs))
	if err != nil {
		return err
	}
	fp := goals.Fingerprint(ctl.status.GetLineState(), ctl.status.GetFirstLine(),
		false, ctl.status.GetHeadCommit(), "unknown", "human-merge")
	_, err = statedb.InsertGoalTriageTx(ctx, tx, statedb.GoalTriage{
		ID: id, Kind: "TRIAGE_KIND_READY_FOR_HUMAN_MERGE", Fingerprint: fp, GoalID: ctl.row.ID,
		Title:      "approved and green on " + ctl.status.GetBranch() + ": the owner merges",
		DetailJSON: `{"work_item":"` + ctl.row.WorkItem + `"}`,
		MachineID:  ctl.row.MachineID,
	}, nowMs, st.HubSeq)
	return err
}
