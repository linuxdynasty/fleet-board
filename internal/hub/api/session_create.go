package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// mintSessionID is a hub-assigned session id (session.proto: assigned before
// the command is sent, so the UI can watch it appear).
func mintSessionID() (string, error) {
	id, err := outbox.NewCommandID()
	if err != nil {
		return "", err
	}
	return "sess_" + id, nil
}

// CreateSession sends EnsureSession (F1). The session id is assigned before
// the command is sent and recorded in hub_sessions, so later calls route to
// the machine that owns it and KillWindow can tell the fleet's own windows
// from the developer's.
func (f *Fleet) CreateSession(ctx context.Context, req *connect.Request[hubv1.CreateSessionRequest],
) (*connect.Response[hubv1.CreateSessionResponse], error) {
	who, err := authorizeSessionsWrite(ctx)
	if err != nil {
		return nil, f.sessRefused(ctx, "", err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	now := f.hubNow()
	if m.GetRequestId() == "" {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required", nil, nil))
	}
	if m.GetMachine().GetValue() == "" {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "machine is required", nil, nil))
	}
	if !sessionNameRule.MatchString(m.GetName()) {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_NAME, "name must match [a-z0-9][a-z0-9-]{0,31}", nil, nil))
	}
	if m.GetServer() == typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED {
		return nil, f.sessRefused(ctx, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "server is required", nil, nil))
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, f.sessFailed(ctx, actor, "encode", err)
	}
	sum := sha256.Sum256(body)
	var sessID string
	var out *hubv1.Operation
	// Telemetry waits for the commit: a job that rolls back must not
	// leave a "sent" count behind.
	var emitKind, emitOutcome string
	var sessOpSeq int64
	var sessRow statedb.SessionRow
	pctx := context.WithoutCancel(ctx) // the publish hook runs on the writer after the handler may have left
	var sessAudits []statedb.AuditStored
	if commitErr := f.store.WriteHook(ctx, statedb.WithAuditCapture(ctx, func(tx *sql.Tx) error {
		if op, replay, err := checkRequest(ctx, tx, actor, m.GetRequestId(), sum[:]); err != nil || replay {
			if err != nil {
				return err
			}
			sessID, err = sessionOfCommand(ctx, tx, op.GetId().GetValue())
			if err != nil {
				return err
			}
			if err := quotaCheckpoint(ctx, quotaWrite); err != nil {
				return err
			}
			out = op
			emitKind, emitOutcome = "ensure_session", "replayed"
			return nil
		}
		id, err := mintSessionID()
		if err != nil {
			return err
		}
		cmd := &agentv1.Command{
			Kind: &agentv1.Command_EnsureSession{EnsureSession: &agentv1.EnsureSession{
				SessionId: &typev1.SessionId{Value: id}, Server: m.GetServer(), Name: m.GetName(),
				Workdir: m.GetWorkdir(), Harness: m.GetHarness(), Reuse: m.GetReuse(),
				WindowName: m.GetWindowName(), OwnerPrincipal: who, Profile: m.GetProfile(),
			}},
		}
		cmd.RequestedBy = who
		cmd.IssuedTime = timestamppb.New(now)
		op, err := outbox.Enqueue(ctx, tx, outbox.In{
			MachineID: m.GetMachine().GetValue(), Command: cmd, RequestID: m.GetRequestId(), Now: now,
			Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
		})
		if err != nil {
			return mapOutboxError(err)
		}
		st, err := statedb.NextStamp(ctx, tx, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO hub_sessions(session_id, machine_id, create_time,
				name, server, owner_id, owner_kind, owner_name, hub_seq)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, m.GetMachine().GetValue(), now.UnixMilli(),
			m.GetName(), serverShort(m.GetServer()),
			who.GetId().GetValue(), roleKindName(who.GetKind()), who.GetName(), st.HubSeq); err != nil {
			return fmt.Errorf("record session: %w", err)
		}
		if err := logRequest(ctx, tx, actor, m.GetRequestId(), op.CommandID, sum[:], now); err != nil {
			return err
		}
		if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
			Actor: actorOf(who), Action: "session.create", Target: "sessions/" + id,
			CommandID: op.CommandID, RequestID: m.GetRequestId(), Now: now,
		}); err != nil {
			return err
		}
		out, err = operationFromOp(ctx, tx, op, m.GetMachine().GetValue(), who, m.GetRequestId(), now)
		if err != nil {
			return err
		}
		sessID = id
		sessOpSeq = op.HubSeq
		// Capture the row in-tx for the Watch publish (#403): a
		// post-commit re-read would see the batch-final row.
		sessRow = statedb.SessionRow{
			SessionID: id, MachineID: m.GetMachine().GetValue(), Name: m.GetName(),
			Server: serverShort(m.GetServer()), OwnerID: who.GetId().GetValue(),
			OwnerKind: roleKindName(who.GetKind()), OwnerName: who.GetName(),
			CreatedMillis: now.UnixMilli(), HubSeq: st.HubSeq,
		}
		emitKind, emitOutcome = op.Kind, "sent"
		return nil
	}, &sessAudits), func() {
		// Runs on the writer in commit order (#393).
		// A request_id replay sends nothing new: only a fresh create
		// publishes its session and command.
		f.publishSessionCreated(pctx, sessRow, out, sessOpSeq, emitOutcome == "replayed")
		// The job's own audits publish here, after its resources, in
		// batch order: a resume never skips them (#403).
		f.publishAudits(sessAudits)
	}); commitErr != nil {
		return nil, f.storeError(ctx, actor, commitErr)
	}
	f.countSession(ctx, emitKind, emitOutcome)
	return connect.NewResponse(&hubv1.CreateSessionResponse{
		Session:   &typev1.SessionId{Value: sessID},
		Operation: out,
	}), nil
}

// sessionOfCommand reads the hub-assigned session id back out of a stored
// EnsureSession command (the CreateSession replay path). A missing or
// unreadable row is the hub's failure, never an empty session id.
func sessionOfCommand(ctx context.Context, tx *sql.Tx, commandID string) (string, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT command FROM commands WHERE command_id = ?`,
		commandID).Scan(&raw); err != nil {
		return "", fmt.Errorf("replay session: %w", err)
	}
	var cmd agentv1.Command
	if err := proto.Unmarshal(raw, &cmd); err != nil {
		return "", fmt.Errorf("replay session: %w", err)
	}
	return cmd.GetEnsureSession().GetSessionId().GetValue(), nil
}
