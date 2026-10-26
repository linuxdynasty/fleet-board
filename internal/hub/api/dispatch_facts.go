package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/ulid"
)

// defaultBranchPattern is the RepoSpec default (03c planning rule 6):
// `fleet/{work}`.
const defaultBranchPattern = "fleet/{work}"

// defaultRepoCap is HubSettings.max_parallel_goals_per_repo when no layer
// sets it (defaults.md).
const defaultRepoCap = 3

// defaultMaxActive and defaultMaxTotal are the machine slot caps when no
// settings layer sets them (defaults.md).
const (
	defaultMaxActive = 2
	defaultMaxTotal  = 4
)

// staleAfter is how old a usage reading may be before the account reads
// STALE and the scheduler stops dispatching on it (03b, defaults.md:
// 3 intervals, 30 min, fixed).
const staleAfter = 30 * time.Minute

// checkGoalInput validates a dispatch input at the API boundary (02b
// "Size limits"): the work item and repo name what to work on, the goal
// text fits the 4 KiB contract cap with control characters stripped but
// newlines and tabs kept (02b strips the same way), and the placement
// names well-formed ids. An empty goal text stays for the caller: until
// issue #63 approves the 0.18 default text, StartGoal refuses it (03c
// DRAFT gates the first StartGoal in such a repo).
func checkGoalInput(in *hubv1.GoalSpecInput) error {
	if in == nil {
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"input is required", nil, nil)
	}
	key := in.GetWorkItem()
	if key.GetIntegration().GetValue() == "" || key.GetProject() == "" || key.GetKey() == "" {
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"input.work_item needs integration, project and key", nil,
			[]*typev1.FieldViolation{{Field: "input.work_item", Description: "integration, project and key are required"}})
	}
	repo := in.GetRepo()
	if repo.GetHost() == "" || repo.GetPath() == "" {
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"input.repo needs host and path", nil,
			[]*typev1.FieldViolation{{Field: "input.repo", Description: "host and path are required"}})
	}
	if len(in.GetGoalText()) > 4096 {
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
			"input.goal_text is over 4 KiB", nil,
			[]*typev1.FieldViolation{{Field: "input.goal_text", Description: "at most 4 KiB"}})
	}
	if strings.ContainsRune(in.GetGoalText(), 0) {
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"input.goal_text holds a NUL byte", nil,
			[]*typev1.FieldViolation{{Field: "input.goal_text", Description: "no NUL bytes"}})
	}
	if len(in.GetDispatchReason()) > 512 {
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
			"input.dispatch_reason is over 512 bytes", nil,
			[]*typev1.FieldViolation{{Field: "input.dispatch_reason", Description: "at most 512 bytes"}})
	}
	if in.GetGoalText() == "" {
		// An explicit objective rides the wire: the #63-approved 0.18
		// default text is what callers send for a repo without hooks
		// (DefaultGoalText, 03c "Default goal text"), never a hub-side
		// fill. Automatic substitution when the repo lacks hooks is
		// follow-up work: the dispatch path has no hook-presence read.
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"input.goal_text is required; send DefaultGoalText for a repo without hooks", nil,
			[]*typev1.FieldViolation{{Field: "input.goal_text", Description: "an objective is required"}})
	}
	place := in.GetPlacement()
	if id := place.GetMachine().GetValue(); id != "" && !hubID.MatchString(id) {
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"placement.machine is not a hub id", nil,
			[]*typev1.FieldViolation{{Field: "input.placement.machine", Description: "1 to 32 characters of a-z, 0-9 and '-'"}})
	}
	if id := place.GetAccount().GetValue(); id != "" && !hubID.MatchString(id) {
		return apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"placement.account is not a hub id", nil,
			[]*typev1.FieldViolation{{Field: "input.placement.account", Description: "1 to 32 characters of a-z, 0-9 and '-'"}})
	}
	return nil
}

// workItemKeyOf renders the canonical mirror id of a dispatch input.
func workItemKeyOf(in *hubv1.GoalSpecInput) string {
	key := in.GetWorkItem()
	kind := strings.TrimPrefix(key.GetKind().String(), "WORK_ITEM_KIND_")
	return statedb.SyncKey(key.GetIntegration().GetValue(), kind, key.GetProject(), key.GetKey())
}

// gatherDispatchFacts reads every hub-check fact inside the decision's
// transaction and evaluates the 03h severity table, so StartGoal and
// PreflightGoal answer from the same committed state (03f). Facts the
// hub does not store yet are documented where they are skipped: the
// harness version check (no inventory table; the agent's preflight
// decides and its failure is BLOCK), pacing states (the row-23 scheduler
// evaluates them; usage staleness warns here), and overage billing (the
// probe verdicts own it).
func (f *Fleet) gatherDispatchFacts(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	in *hubv1.GoalSpecInput, acked []typev1.ReasonCode, now time.Time,
) (*dispatchFacts, error) {
	fx := &dispatchFacts{itemKey: workItemKeyOf(in)}
	item, err := statedb.GetWorkItemStateTx(ctx, tx, fx.itemKey)
	if err != nil {
		if err == statedb.ErrWorkItemUnknown {
			return nil, apiError(connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
				"unknown work item", nil, nil)
		}
		return nil, fmt.Errorf("dispatch: read work item: %w", err)
	}
	fx.item = item
	var trust hubv1.TrustVerdict
	if err := protojson.Unmarshal([]byte(item.TrustJSON), &trust); err != nil {
		return nil, fmt.Errorf("dispatch: read work item trust: %w", err)
	}
	stage := hubv1.WorkStage(item.Stage)
	if stage == hubv1.WorkStage_WORK_STAGE_CLOSED {
		return nil, apiError(connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the work item is closed", nil,
			[]*typev1.FieldViolation{{Field: "input.work_item", Description: "the item is closed"}})
	}
	var input goals.Input
	input.WorkItemUntrusted = !trust.GetTrusted()
	input.WorkItemClaimed = stage == hubv1.WorkStage_WORK_STAGE_CLAIMED_ELSEWHERE || foreignClaim(item.ClaimsJSON)
	if _, busy, err := statedb.LiveGoalForWorkItemTx(ctx, tx, fx.itemKey); err != nil || busy {
		if err != nil {
			return nil, fmt.Errorf("dispatch: check live goal: %w", err)
		}
		input.WorkItemBusy = true
	}
	repo, err := statedb.ReadRepoTx(ctx, tx, in.GetRepo().GetHost(), in.GetRepo().GetPath())
	if err != nil && err != statedb.ErrRepoUnknown {
		return nil, fmt.Errorf("dispatch: read repo: %w", err)
	}
	var spec hubv1.RepoSpec
	if err == nil {
		if err := protojson.Unmarshal(repo.Spec, &spec); err != nil {
			return nil, fmt.Errorf("dispatch: read repo spec: %w", err)
		}
		// A Repo row the hub holds but disabled is not watched (03c
		// planning rule 1: the repo enabled and below its cap).
		input.RepoWatched = spec.GetEnabled()
	}
	pattern := spec.GetBranchPattern()
	if pattern == "" {
		pattern = defaultBranchPattern
	}
	fx.display = workItemDisplay(in)
	fx.branch = renderBranch(pattern, fx.display, "")
	if _, busy, err := statedb.LiveGoalForBranchTx(ctx, tx,
		in.GetRepo().GetHost()+"/"+in.GetRepo().GetPath(), fx.branch); err != nil || busy {
		if err != nil {
			return nil, fmt.Errorf("dispatch: check branch goal: %w", err)
		}
		input.BranchBusy = true
	}
	repoCap := int64(defaultRepoCap)
	if v := spec.MaxParallelGoals; v != nil {
		repoCap = int64(*v)
	} else if hub := fleetHubSpec(ctx, tx); hub != nil {
		if v := hub.MaxParallelGoalsPerRepo; v != nil {
			repoCap = int64(*v)
		}
	}
	if n, err := statedb.CountRepoLiveGoalsTx(ctx, tx,
		in.GetRepo().GetHost()+"/"+in.GetRepo().GetPath()); err != nil {
		return nil, fmt.Errorf("dispatch: count repo goals: %w", err)
	} else if n >= repoCap {
		input.RepoAtCap = true
	}
	account, consent, err := f.dispatchConsent(ctx, tx, in, &spec)
	if err != nil {
		return nil, err
	}
	fx.consent = consent
	fx.merge = hasConsent(consent, typev1.ConsentAct_CONSENT_ACT_MERGE)
	if len(consent) == 0 {
		input.ConsentEmpty = true
	}
	_ = account
	if err := f.dispatchMachine(ctx, tx, in, &input, fx, now.UnixMilli()); err != nil {
		return nil, err
	}
	if err := f.dispatchIntegration(ctx, tx, in, &input, now); err != nil {
		return nil, err
	}
	if err := f.dispatchAccount(ctx, tx, in, &input, fx, now); err != nil {
		return nil, err
	}
	fx.fillSpecFacts(in)
	fx.sessionID, err = ulid.New(now, f.random)
	if err != nil {
		return nil, fmt.Errorf("dispatch: mint session id: %w", err)
	}
	fx.checks = goals.Evaluate(input)
	fx.decision = goals.DecideStart(fx.checks, acked)
	return fx, nil
}

// foreignClaim reports whether the stored claims name a claim by someone
// other than this fleet (03m "double_claim", 03h WORK_ITEM_CLAIMED).
func foreignClaim(raw string) bool {
	var claims []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &claims); err != nil {
		return false
	}
	for _, c := range claims {
		var cl integrationv1.Claim
		if err := protojson.Unmarshal(c, &cl); err != nil {
			continue
		}
		if !cl.GetByFleet() {
			return true
		}
	}
	return false
}
