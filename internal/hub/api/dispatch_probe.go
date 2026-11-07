package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/ulid"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// probeAuditAction is the audit action of a ProbeAccount call (02m).
const probeAuditAction = "account.probe"

// ProbeAccount runs the account's probe now once an account key is
// stored: it enqueues EnsureProbe with run_now on an ONLINE machine
// that may launch the harness, preferring other machines over the
// hub's. Until the hub stores account keys it refuses NO_FIT_MACHINE
// instead of queueing a probe the agent would refuse. The run_now gate
// allows one per account per minute, and a repeated request_id returns
// the stored operation instead of queueing a second probe.
func (f *Fleet) ProbeAccount(ctx context.Context, req *connect.Request[hubv1.ProbeAccountRequest],
) (*connect.Response[hubv1.ProbeAccountResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.probeRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.probeRefusedAs(ctx, actor, err)
	}
	if m.GetId() == nil {
		return nil, f.probeRefusedAs(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "account id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "an account id is required"}}))
	}
	now := f.hubNow()
	var out *hubv1.Operation
	var queued outbox.Op
	var dispAudits []statedb.AuditStored
	doneCh, submitErr := f.store.SubmitHook(ctx, statedb.WithAuditCapture(ctx, func(tx *sql.Tx) error {
		if id, approvalID, replay, err := f.checkProbeReplay(ctx, tx, actor, m); err != nil || replay {
			if err != nil {
				return err
			}
			if approvalID != "" {
				return apiError(connect.CodeAlreadyExists,
					typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
					"this request_id approved a dispatch: probe with a fresh one", nil, nil)
			}
			stored, err := loadOperation(ctx, tx, id)
			if err != nil {
				return err
			}
			out = stored
			return nil
		}
		stored, err := statedb.ReadAccountTx(ctx, tx, m.GetId().GetValue())
		if err != nil {
			if errors.Is(err, statedb.ErrAccountUnknown) {
				return apiError(connect.CodeNotFound,
					typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown account", nil, nil)
			}
			return fmt.Errorf("probe: read account: %w", err)
		}
		machineID, err := f.probeMachine(ctx, tx, stored.Harness)
		if err != nil {
			return err
		}
		// The gate is peeked here and claimed in the commit hook, so
		// a failed commit leaves a retry free to probe.
		if !f.peekProbeRunNow(m.GetId().GetValue(), now) {
			return apiError(connect.CodeFailedPrecondition,
				typev1.ReasonCode_REASON_CODE_RATE_LIMITED,
				"the account probed less than a minute ago: its reading is current", nil, nil)
		}
		probeID, err := ulid.New(now, f.random)
		if err != nil {
			return err
		}
		key, err := f.probeAccountKey(ctx, tx, m.GetId().GetValue(), machineID)
		if err != nil {
			return err
		}
		cmd := &agentv1.Command{
			Kind: &agentv1.Command_EnsureProbe{EnsureProbe: &agentv1.EnsureProbe{
				ProbeId:    &typev1.ProbeId{Value: probeID},
				AccountId:  m.GetId(),
				AccountKey: &typev1.AccountKey{Value: key},
				Harness:    &typev1.Harness{Name: stored.Harness},
				Interval:   durationpb.New(probeInterval),
				RunNow:     true,
			}},
		}
		cmd.RequestedBy = who
		cmd.IssuedTime = timestamppb.New(now)
		op, err := outbox.Enqueue(ctx, tx, outbox.In{
			MachineID: machineID, Command: cmd, RequestID: m.GetRequestId(), Now: now,
			Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
		})
		if err != nil {
			return mapOutboxError(err)
		}
		if err := logRequest(ctx, tx, actor, m.GetRequestId(), op.CommandID, dispatchHash(m), now); err != nil {
			return err
		}
		if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
			Actor: actorOf(who), Action: probeAuditAction, Target: "accounts/" + m.GetId().GetValue(),
			CommandID: op.CommandID, RequestID: m.GetRequestId(), Now: now,
		}); err != nil {
			return err
		}
		out, err = operationFromOp(ctx, tx, op, machineID, who, m.GetRequestId(), now)
		if err != nil {
			return err
		}
		queued = op
		return nil
	}, &dispAudits), func() {
		if queued.Created {
			f.markProbeRunNow(m.GetId().GetValue(), now)
			f.publishOperationWatch(queued.HubSeq, out)
		}
		f.publishAudits(dispAudits)
	})
	if submitErr != nil {
		return nil, f.probeStoreError(ctx, actor, submitErr)
	}
	if commitErr := <-doneCh; commitErr != nil {
		return nil, f.probeStoreError(ctx, actor, commitErr)
	}
	if queued.Created && f.onCommandQueued != nil {
		f.onCommandQueued(queued)
	}
	f.countDispatch(ctx, "probe", probeResultOf(out))
	f.tel.Logger.InfoContext(ctx, "dispatch.planned", "actor", actor,
		"account", m.GetId().GetValue(), "machine", out.GetMachine().GetValue(), "mode", "probe")
	return connect.NewResponse(&hubv1.ProbeAccountResponse{Operation: out}), nil
}

// probeAccountKey resolves the expected account identity key for an
// EnsureProbe (command_tmux.proto): the agent refuses a probe whose key
// is empty or mismatched. It returns the key, or empty (with the
// NO_FIT_MACHINE refusal) while unresolvable: the hub stores no account
// keys yet. A later row that stores observed keys fills this in and
// scopes the machine to one with that identity signed in; both
// EnsureProbe constructors already carry the resolved key.
func (f *Fleet) probeAccountKey(ctx context.Context, tx *sql.Tx, accountID, machineID string) (string, error) {
	return "", apiError(connect.CodeFailedPrecondition,
		typev1.ReasonCode_REASON_CODE_NO_FIT_MACHINE,
		"the hub does not store account keys yet, so it cannot build a probe the agent accepts", nil, nil)
}

// probeMachine picks the probe's machine (03b probe assignment, 03c
// rule 3): ONLINE, not draining, launch-allowed for the harness,
// preferring other machines over the hub's, then most free active
// slots. NO_FIT_MACHINE when nothing qualifies.
func (f *Fleet) probeMachine(ctx context.Context, tx *sql.Tx, harness string) (string, error) {
	machines, err := statedb.ListMachineDispatchTx(ctx, tx)
	if err != nil {
		return "", err
	}
	rows, err := statedb.ListMachineRowsTx(ctx, tx)
	if err != nil {
		return "", err
	}
	facts := map[string]string{}
	for _, r := range rows {
		facts[r.MachineID] = r.Facts
	}
	hubID := hubMachineID(facts, f.hubHostname)
	type fit struct {
		id   string
		free int64
	}
	var fits []fit
	for _, m := range machines {
		if m.State != statedb.MachineStateOnline || m.Draining {
			continue
		}
		invs, err := statedb.ListMachineHarnessTx(ctx, tx, m.ID)
		if err != nil {
			return "", err
		}
		allowed := false
		for _, inv := range invs {
			if inv.Harness == harness && inv.LaunchAllowed {
				allowed = true
				break
			}
		}
		if !allowed {
			continue
		}
		maxActive, maxTotal, err := machineSlotCaps(ctx, tx, m.ID)
		if err != nil {
			return "", err
		}
		if maxActive <= 0 {
			maxActive = int64(defaultMaxActive)
		}
		if maxTotal <= 0 {
			maxTotal = int64(defaultMaxTotal)
		}
		// Both slot caps bind the probe like a start (03c rule 3): a
		// machine at either cap takes no new work.
		if m.Active >= maxActive || m.Total >= maxTotal {
			continue
		}
		fits = append(fits, fit{id: m.ID, free: maxActive - m.Active})
	}
	if len(fits) == 0 {
		return "", apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_NO_FIT_MACHINE,
			"no ONLINE machine may launch the harness right now", nil, nil)
	}
	best := fits[0]
	for _, c := range fits[1:] {
		cHub, bHub := c.id == hubID, best.id == hubID
		if cHub != bHub {
			if bHub {
				best = c
			}
			continue
		}
		if c.free > best.free || (c.free == best.free && c.id < best.id) {
			best = c
		}
	}
	return best.id, nil
}

// checkProbeReplay replays a repeated ProbeAccount request_id: the
// stored Operation comes back and nothing is queued twice (02b
// idempotency). A different body with the same key is
// REQUEST_ID_CONFLICT.
func (f *Fleet) checkProbeReplay(ctx context.Context, tx *sql.Tx, principal string,
	m *hubv1.ProbeAccountRequest,
) (commandID, approvalID string, replay bool, err error) {
	var stored []byte
	err = tx.QueryRowContext(ctx, `SELECT command_id, approval_id, request_hash FROM request_log
		WHERE principal_id = ? AND request_id = ?`, principal, m.GetRequestId()).Scan(&commandID, &approvalID, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if string(stored) != string(dispatchHash(m)) {
		return "", "", false, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	}
	return commandID, approvalID, true, nil
}

// probeResultOf names the probe outcome for the dispatch counter.
func probeResultOf(out *hubv1.Operation) string {
	if out == nil {
		return "failed"
	}
	return "sent"
}

// probeRefused logs a probe caller error and returns it.
func (f *Fleet) probeRefused(ctx context.Context, err error) error {
	f.countDispatch(ctx, "probe", "refused")
	f.tel.Logger.InfoContext(ctx, "dispatch.issue_refused", "reason", reasonOf(err))
	return err
}

// probeRefused logs a probe caller error with the actor.
func (f *Fleet) probeRefusedAs(ctx context.Context, actor string, err error) error {
	f.countDispatch(ctx, "probe", "refused")
	f.tel.Logger.InfoContext(ctx, "dispatch.issue_refused", "reason", reasonOf(err), "actor", actor)
	return err
}

// probeStoreError renders a writer-job failure: caller errors stay as
// refused, the hub's own become INTERNAL.
func (f *Fleet) probeStoreError(ctx context.Context, actor string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return f.probeRefusedAs(ctx, actor, err)
	}
	f.countDispatch(ctx, "probe", "failed")
	kind := "io"
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "dispatch.failed", "step", "store", "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not probe the account", nil, nil)
}
