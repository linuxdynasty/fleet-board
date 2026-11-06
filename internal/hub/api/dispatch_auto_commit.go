package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/pacing/probe"
	"github.com/linuxdynasty/fleet-board/internal/hub/scheduler"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/ulid"
)

// commitDispatch commits one planned dispatch: mint the goal id, render
// the branch and the 0.18 default text, re-run the hub checks, and on a
// clean StartOK insert the STARTING goal, the outbox StartGoal and the
// audit in one transaction, then queue the account's run_now probe
// behind the start (03c W5). A refused check skips the item with its
// reason; the unique backstop maps to the busy skip.
func (f *Fleet) commitDispatch(ctx context.Context, who *typev1.Principal, d scheduler.Dispatch, item itemCtx, now time.Time) (string, string, error) {
	goalID, err := ulid.New(now, f.random)
	if err != nil {
		return "", "", fmt.Errorf("scheduler: mint goal id: %w", err)
	}
	repo := item.repoHost + "/" + item.repoPath
	branch := renderBranch(d.BranchPattern, d.Display, goalID)
	text, _ := goals.DefaultGoalText(repo, item.number, branch)
	in := &hubv1.GoalSpecInput{
		WorkItem: &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: item.integration},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
			Project:     item.project, Key: item.itemKey,
		},
		Repo:     &typev1.RepoRef{Host: item.repoHost, Path: item.repoPath},
		GoalText: text,
		Placement: &hubv1.GoalPlacement{
			Machine: &typev1.MachineId{Value: d.MachineID},
			Harness: &typev1.Harness{Name: d.Harness},
			Account: &typev1.AccountId{Value: d.AccountID},
		},
	}
	m := &hubv1.StartGoalRequest{RequestId: "auto-" + goalID, Input: in}
	var goalAfter statedb.GoalRow
	var itemAfter statedb.WorkItemRow
	var queued outbox.Op
	var goalOp *hubv1.Operation
	var dispAudits []statedb.AuditStored
	doneCh, submitErr := f.store.SubmitHook(ctx, statedb.WithAuditCapture(ctx, func(tx *sql.Tx) error {
		fx, err := f.gatherDispatchFacts(ctx, tx, who, in, nil, goalID, now)
		if err != nil {
			return err
		}
		if fx.decision.Outcome != goals.StartOK {
			return &skipPass{reason: checkSkipReason(fx.decision)}
		}
		after, operation, op, err := f.insertAutoTx(ctx, tx, who, m, fx, goalID, d, now)
		if err != nil {
			return err
		}
		goalAfter, queued, goalOp = after, op, operation
		itemAfter, err = readWorkItemForPublish(ctx, tx, d.ItemKey)
		return err
	}, &dispAudits), func() {
		if queued.Created {
			// The commit landed: claim the account's run_now the
			// peek inside the transaction held open.
			f.markProbeRunNow(d.AccountID, now)
			f.publishOperationWatch(queued.HubSeq, goalOp)
			f.PublishGoalsCommitted(ctx, d.MachineID, nil, []statedb.GoalRow{goalAfter})
			f.publishDispatchWorkItem(ctx, itemAfter)
		}
		f.publishAudits(dispAudits)
	})
	if submitErr != nil {
		reason, err := f.commitSkipOrFail(submitErr)
		if err != nil {
			return "", "", err
		}
		return "", reason, nil
	}
	if commitErr := <-doneCh; commitErr != nil {
		reason, err := f.commitSkipOrFail(commitErr)
		if err != nil {
			return "", "", err
		}
		return "", reason, nil
	}
	if queued.Created && f.onCommandQueued != nil {
		f.onCommandQueued(queued)
	}
	f.tel.Logger.InfoContext(ctx, "dispatch.planned", "actor", "scheduler",
		"goal", goalID, "work_item", d.ItemKey, "machine", d.MachineID,
		"account", d.AccountID, "mode", "auto")
	return goalID, "", nil
}

// commitSkipOrFail maps a commit-transaction refusal to its skip reason:
// a refused check (the world changed between snapshot and commit, so
// the next pass retries) and the two busy races skip with their reason.
// Every other failure stays a real error and aborts the pass: only a
// busy race means "wait for the next pass"; a closed or missing item, a
// bad write or a dead store must surface, never read as a wait (P3-2).
func (f *Fleet) commitSkipOrFail(err error) (string, error) {
	var sp *skipPass
	if errors.As(err, &sp) {
		if sp.reason == "" {
			return "unknown_check", nil
		}
		return sp.reason, nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		switch reasonOf(err) {
		case typev1.ReasonCode_REASON_CODE_WORK_ITEM_BUSY.String(),
			typev1.ReasonCode_REASON_CODE_BRANCH_BUSY.String():
			return checkCodeName(reasonOf(err)), nil
		}
	}
	return "", err
}

// insertAutoTx inserts the STARTING goal with its outbox StartGoal and
// audit row like a manual dispatch, then queues the account's run_now
// probe behind the start (03c W5: the scheduler queues EnsureProbe for
// the account). The run_now gate allows one per account per minute.
func (f *Fleet) insertAutoTx(ctx context.Context, tx *sql.Tx, who *typev1.Principal,
	m *hubv1.StartGoalRequest, fx *dispatchFacts, goalID string, d scheduler.Dispatch, now time.Time,
) (statedb.GoalRow, *hubv1.Operation, outbox.Op, error) {
	after, operation, op, err := f.insertDispatchTx(ctx, tx, who, m, fx, goalID, now)
	if err != nil {
		return statedb.GoalRow{}, nil, outbox.Op{}, err
	}
	// The run_now gate is peeked here and claimed in the commit hook, so
	// a failed commit leaves the next pass free to probe.
	if f.peekProbeRunNow(d.AccountID, now) {
		// probeAccountKey is empty while the hub stores no keys: the
		// goal still dispatches, without the probe the agent would
		// refuse.
		key, _ := f.probeAccountKey(ctx, tx, d.AccountID, d.MachineID)
		if key == "" {
			return after, operation, op, nil
		}
		probeID, err := ulid.New(now, f.random)
		if err != nil {
			return statedb.GoalRow{}, nil, outbox.Op{}, fmt.Errorf("scheduler: mint probe id: %w", err)
		}
		cmd := &agentv1.Command{
			Kind: &agentv1.Command_EnsureProbe{EnsureProbe: &agentv1.EnsureProbe{
				ProbeId:    &typev1.ProbeId{Value: probeID},
				AccountId:  &typev1.AccountId{Value: d.AccountID},
				AccountKey: &typev1.AccountKey{Value: key},
				Harness:    &typev1.Harness{Name: d.Harness},
				Interval:   durationpb.New(probeInterval),
				RunNow:     true,
			}},
		}
		cmd.RequestedBy = who
		cmd.IssuedTime = timestamppb.New(now)
		if _, err := outbox.Enqueue(ctx, tx, outbox.In{
			MachineID: d.MachineID, Command: cmd, RequestID: m.GetRequestId(),
			GoalID: goalID, Now: now,
			Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
		}); err != nil {
			return statedb.GoalRow{}, nil, outbox.Op{}, fmt.Errorf("scheduler: queue probe: %w", err)
		}
	}
	return after, operation, op, nil
}

// peekProbeRunNow reports whether the account may run_now without
// claiming it; markProbeRunNow claims it. At most one run_now per
// account per minute (probe.GateRunNow, 03b).
func (f *Fleet) peekProbeRunNow(account string, now time.Time) bool {
	f.schedMu.Lock()
	defer f.schedMu.Unlock()
	last, ok := f.probeRunNow[account]
	return !ok || probe.GateRunNow(now, last)
}

// markProbeRunNow claims the account's run_now. Callers peek inside the
// transaction and mark in the commit hook, so a failed commit leaves
// the gate open.
func (f *Fleet) markProbeRunNow(account string, now time.Time) {
	f.schedMu.Lock()
	defer f.schedMu.Unlock()
	f.probeRunNow[account] = now
}

// checkSkipReason names the first refusing check for the skip counter.
func checkSkipReason(d goals.Decision) string {
	for _, c := range d.Blocks {
		return checkCodeName(c.GetCheck().GetReason().String())
	}
	for _, c := range d.Confirms {
		return checkCodeName(c.GetCheck().GetReason().String())
	}
	for _, c := range d.Unacked {
		return checkCodeName(c.GetCheck().GetReason().String())
	}
	return "unknown_check"
}

// checkCodeName lower-cases a ReasonCode short name for the skip label
// (fleet_hub_scheduler_skips_total{reason}, 03c): the enum names are
// closed, so the label space stays bounded.
func checkCodeName(reason string) string {
	return strings.ToLower(strings.TrimPrefix(reason, "REASON_CODE_"))
}
