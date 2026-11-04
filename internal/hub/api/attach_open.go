package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/ulid"
)

// attachDims bounds window sizes: a terminal wider or taller than this is
// a malformed open, not a display.
const (
	attachMinDim = 1
	attachMaxDim = 1000
)

// defaultAttachIdle is attach_idle_timeout when the owner never set one
// (defaults.md): 30 minutes.
const defaultAttachIdle = 30 * time.Minute

// defaultAttachReadyWait bounds the wait for the agent's TermReady: the
// command's own 2-minute deadline still applies, and a late agent meets a
// closed bridge and stands down instead of orphaning a view session.
const defaultAttachReadyWait = 60 * time.Second

// defaultAttachCloseWait bounds the wait for the agent's TermUp closed
// after a client detach before the hub falls back to CloseAttach.
const defaultAttachCloseWait = 5 * time.Second

// validAttachDims reports whether cols and rows name a real terminal.
func validAttachDims(cols, rows uint32) bool {
	return cols >= attachMinDim && cols <= attachMaxDim && rows >= attachMinDim && rows <= attachMaxDim
}

// attachIdleTx resolves how long an attach may sit with no input or
// output before the hub detaches it IDLE_TIMEOUT: the fleet layer's
// values.attach_idle_timeout, or the documented default when the owner
// never set one or set a non-positive wait. Mirrors approvalTimeoutTx:
// an undecodable layer fails the open instead of guessing, and a wait
// time.Duration cannot stamp falls back closed the same way.
func attachIdleTx(ctx context.Context, tx *sql.Tx) (time.Duration, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT spec_json FROM settings_layers WHERE scope = ?`,
		statedb.FleetScope).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return defaultAttachIdle, nil
		}
		return 0, fmt.Errorf("attach: read idle timeout: %w", err)
	}
	var spec hubv1.SettingsSpec
	if err := protojson.Unmarshal([]byte(raw), &spec); err != nil {
		return 0, fmt.Errorf("attach: read idle timeout: %w", err)
	}
	d := spec.GetValues().GetAttachIdleTimeout()
	if d == nil {
		return defaultAttachIdle, nil
	}
	if err := d.CheckValid(); err != nil {
		return 0, fmt.Errorf("attach: read idle timeout: %w", err)
	}
	if !timeoutRepresentable(d.GetSeconds(), d.GetNanos()) {
		return 0, fmt.Errorf("attach: idle timeout %ds+%dns overflows time.Duration", d.GetSeconds(), d.GetNanos())
	}
	if d.AsDuration() <= 0 {
		return defaultAttachIdle, nil
	}
	return d.AsDuration(), nil
}

// sendOpenAttach opens one attach: it claims the (principal, request_id)
// once-only slot (a retry adopts the live bridge instead of opening
// twice), resolves the session's machine and tmux name, and enqueues
// OpenAttach audited as attach.open, all in one writer job. A refused or
// failed open removes its bridge and forgets the claim, so a retry starts
// clean. adopted reports a retry that rejoined a live bridge: no command
// was enqueued for it.
func (f *Fleet) sendOpenAttach(ctx context.Context, who *typev1.Principal, open *hubv1.AttachOpen,
	hash []byte, readOnly bool,
) (*attach.Bridge, string, bool, time.Duration, error) {
	actor := who.GetId().GetValue()
	now := f.hubNow()
	attachID, err := ulid.New(now, f.random)
	if err != nil {
		return nil, "", false, 0, f.attachFailed(ctx, actor, "id", err)
	}
	b, adopted, err := f.attach.OpenOrAdopt(actor, open.GetRequestId(), sha256.Sum256(hash), attachID,
		"", open.GetSession().GetValue(), readOnly)
	if err != nil {
		if errors.Is(err, attach.ErrOpenRaced) {
			return nil, "", false, 0, f.attachRefused(ctx, actor, apiError(connect.CodeInternal,
				typev1.ReasonCode_REASON_CODE_ATTACH_NOT_FOUND, "the attach raced a close; open again", nil, nil))
		}
		return nil, "", false, 0, f.attachRefused(ctx, actor, apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil))
	}
	if adopted {
		f.countAttach(ctx, "opened", "")
		return b, b.CommandID(), true, b.Idle(), nil
	}
	cleanup := func() {
		f.attach.Remove(b)
		f.attach.ForgetOpen(actor, open.GetRequestId(), attachID)
	}
	var commandID string
	var opMsg *hubv1.Operation
	var idle time.Duration
	var sessAudits []statedb.AuditStored
	var sessOp outbox.Op
	doneCh, submitErr := f.store.SubmitHook(ctx, statedb.WithAuditCapture(ctx, func(tx *sql.Tx) error {
		machine, target, err := resolveAttachSession(ctx, tx, open.GetSession().GetValue())
		if err != nil {
			return err
		}
		// An offline agent never readies: refuse fast instead of
		// hanging the browser past the ready wait. A death between
		// this check and the enqueue still meets the wait's cleanup.
		if f.machineLive == nil || !f.machineLive(machine) {
			return apiError(connect.CodeUnavailable,
				typev1.ReasonCode_REASON_CODE_MACHINE_OFFLINE, "the machine is offline", nil, nil)
		}
		idle, err = attachIdleTx(ctx, tx)
		if err != nil {
			return err
		}
		cmd := &agentv1.Command{
			Kind: &agentv1.Command_OpenAttach{OpenAttach: &agentv1.OpenAttach{
				AttachId: &typev1.AttachId{Value: attachID}, Session: target,
				Cols: open.GetCols(), Rows: open.GetRows(), ReadOnly: readOnly,
			}},
			Requires: &typev1.Capability{Name: "attach.pty", Version: 1},
		}
		cmd.RequestedBy = who
		cmd.IssuedTime = timestamppb.New(now)
		op, err := outbox.Enqueue(ctx, tx, outbox.In{
			MachineID: machine, Command: cmd, RequestID: open.GetRequestId(), Now: now,
			Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
		})
		if err != nil {
			return mapOutboxError(err)
		}
		if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
			Actor: actorOf(who), Action: "attach.open", Target: "sessions/" + open.GetSession().GetValue(),
			CommandID: op.CommandID, RequestID: open.GetRequestId(), Now: now,
		}); err != nil {
			return err
		}
		opMsg, err = operationFromOp(ctx, tx, op, machine, who, open.GetRequestId(), now)
		if err != nil {
			return err
		}
		b.SetMachine(machine)
		b.SetCommand(op.CommandID)
		commandID = op.CommandID
		sessOp = op
		return nil
	}, &sessAudits), func() {
		// Runs on the writer in commit order: a fresh queue publishes
		// its operation like every other command.
		if sessOp.Created {
			f.publishOperationWatch(sessOp.HubSeq, opMsg)
		}
		f.publishAudits(sessAudits)
	})
	if submitErr != nil {
		cleanup()
		return nil, "", false, 0, f.attachStoreError(ctx, actor, submitErr)
	}
	if commitErr := <-doneCh; commitErr != nil {
		cleanup()
		return nil, "", false, 0, f.attachStoreError(ctx, actor, commitErr)
	}
	if sessOp.Created && f.onCommandQueued != nil {
		f.onCommandQueued(sessOp)
	}
	f.countSession(ctx, "open_attach", "sent")
	f.countAttach(ctx, "opened", "")
	f.tel.Logger.InfoContext(ctx, "attach.opened", "attach_id", attachID,
		"machine", sessOp.MachineID, "command", commandID, "actor", actor)
	b.SetIdle(idle)
	return b, commandID, false, idle, nil
}

// resolveAttachSession maps a hub session to its machine and the tmux
// target the agent attaches: the row's tmux name on its server. Sessions
// the hub did not start are not routable yet and fail SESSION_NOT_FOUND.
func resolveAttachSession(ctx context.Context, tx *sql.Tx, sessionID string) (string, *agentv1.SessionTarget, error) {
	var machine, name, server string
	err := tx.QueryRowContext(ctx, `SELECT machine_id, name, server FROM hub_sessions WHERE session_id = ?`,
		sessionID).Scan(&machine, &name, &server)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil, apiError(connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
			"the hub did not start this session and cannot route to it yet", nil, nil)
	case err != nil:
		return "", nil, fmt.Errorf("resolve attach session: %w", err)
	case machine == "" || name == "":
		return "", nil, apiError(connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
			"the hub did not start this session and cannot route to it yet", nil, nil)
	}
	return machine, &agentv1.SessionTarget{
		Server: serverKind(server),
		By:     &agentv1.SessionTarget_Name{Name: name},
	}, nil
}

// attachWaits returns the agent-confirm allowances, defaulted.
func (f *Fleet) attachWaits() (readyWait, closeWait time.Duration) {
	return f.attachReadyWait, f.attachCloseWait
}

// serverKind parses a hub_sessions.server short name; pre-detail rows
// carry none and mean the developer's own server.
func serverKind(server string) typev1.TmuxServerKind {
	switch server {
	case "FLEET":
		return typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET
	case "PROBE":
		return typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE
	default:
		return typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT
	}
}

// auditAttachClose stamps the attach.close row in its own writer job: the
// session that ended, the OpenAttach command that made it, and the close
// reason in the detail. Audit failures are logged, never returned: the
// close already happened.
func (f *Fleet) auditAttachClose(ctx context.Context, who *typev1.Principal, sessionID, commandID, requestID string,
	reason typev1.AttachCloseReason,
) {
	actor := who.GetId().GetValue()
	now := f.hubNow()
	doneCh, submitErr := f.store.SubmitHook(ctx, func(tx *sql.Tx) error {
		_, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
			Actor: actorOf(who), Action: "attach.close", Target: "sessions/" + sessionID,
			CommandID: commandID, RequestID: requestID, Now: now,
			Detail: `{"close_reason":` + strconv.Quote(strings.TrimPrefix(reason.String(),
				"ATTACH_CLOSE_REASON_")) + `}`,
		})
		return err
	}, func() {})
	if submitErr != nil {
		f.tel.Logger.ErrorContext(ctx, "attach.close_audit_failed", "step", "submit", "actor", actor)
		return
	}
	select {
	case err := <-doneCh:
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "attach.close_audit_failed", "step", "commit", "actor", actor)
		}
	case <-ctx.Done():
	}
}

// enqueueCloseAttach sends CloseAttach after the agent missed its TermUp
// closed: killing the PTY tears the view session down either way, and
// the target pane keeps running. Best effort: the close already
// happened, so failures are only logged.
func (f *Fleet) enqueueCloseAttach(ctx context.Context, who *typev1.Principal, machine, attachID, requestID string) {
	now := f.hubNow()
	cmd := &agentv1.Command{
		Kind: &agentv1.Command_CloseAttach{CloseAttach: &agentv1.CloseAttach{
			AttachId: &typev1.AttachId{Value: attachID},
		}},
		Requires:    &typev1.Capability{Name: "attach.pty", Version: 1},
		RequestedBy: who,
		IssuedTime:  timestamppb.New(now),
	}
	doneCh, submitErr := f.store.SubmitHook(ctx, func(tx *sql.Tx) error {
		_, err := outbox.Enqueue(ctx, tx, outbox.In{
			MachineID: machine, Command: cmd, RequestID: requestID + "/close", Now: now,
			Checkpoint: func() error { return quotaCheckpoint(ctx, quotaWrite) },
		})
		if err != nil {
			return mapOutboxError(err)
		}
		return nil
	}, func() {})
	if submitErr != nil {
		f.tel.Logger.ErrorContext(ctx, "attach.close_command_failed", "step", "submit", "attach_id", attachID)
		return
	}
	select {
	case err := <-doneCh:
		if err != nil {
			f.tel.Logger.ErrorContext(ctx, "attach.close_command_failed", "step", "commit", "attach_id", attachID)
		}
	case <-ctx.Done():
	}
}

// wsCodeFor maps an attach end to its 02f WebSocket close code. Clean
// reasons follow the reason table; ERROR consults the typed detail: a
// policy refusal closes 1008, a retryable failure 1013, a malformed
// message 1007, anything else 1011.
func wsCodeFor(closed *hubv1.AttachClosed) int {
	if closed.GetReason() != typev1.AttachCloseReason_ATTACH_CLOSE_REASON_ERROR {
		return attach.CloseCode(closed.GetReason())
	}
	switch closed.GetError().GetReason() {
	case typev1.ReasonCode_REASON_CODE_ATTACH_TICKET_INVALID,
		typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
		typev1.ReasonCode_REASON_CODE_CREDENTIALS_EXPIRED,
		typev1.ReasonCode_REASON_CODE_SCOPE_MISSING,
		typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT,
		typev1.ReasonCode_REASON_CODE_DEVICE_NOT_ACTIVE,
		typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
		typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND:
		return 1008
	case typev1.ReasonCode_REASON_CODE_RATE_LIMITED,
		typev1.ReasonCode_REASON_CODE_MACHINE_OFFLINE:
		return 1013
	case typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING,
		typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE:
		return 1007
	default:
		return 1011
	}
}

// connectErrorFor maps an ERROR close to the Connect error the HTTP/2
// caller gets instead of a frame: the trailer carries the typed detail.
func connectErrorFor(closed *hubv1.AttachClosed) error {
	detail := closed.GetError()
	if detail == nil {
		detail = &typev1.ErrorDetail{Reason: typev1.ReasonCode_REASON_CODE_INTERNAL}
	}
	code := connect.CodeInternal
	switch detail.GetReason() {
	case typev1.ReasonCode_REASON_CODE_ATTACH_TICKET_INVALID,
		typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
		typev1.ReasonCode_REASON_CODE_CREDENTIALS_EXPIRED:
		code = connect.CodeUnauthenticated
	case typev1.ReasonCode_REASON_CODE_SCOPE_MISSING,
		typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT,
		typev1.ReasonCode_REASON_CODE_DEVICE_NOT_ACTIVE:
		code = connect.CodePermissionDenied
	case typev1.ReasonCode_REASON_CODE_MACHINE_OFFLINE:
		code = connect.CodeUnavailable
	case typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
		typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
		typev1.ReasonCode_REASON_CODE_ATTACH_NOT_FOUND:
		code = connect.CodeNotFound
	case typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
		typev1.ReasonCode_REASON_CODE_RESOURCE_ALREADY_EXISTS,
		typev1.ReasonCode_REASON_CODE_SECRET_ALREADY_DELIVERED:
		code = connect.CodeAlreadyExists
	case typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING,
		typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE:
		code = connect.CodeInvalidArgument
	case typev1.ReasonCode_REASON_CODE_RATE_LIMITED:
		code = connect.CodeResourceExhausted
	case typev1.ReasonCode_REASON_CODE_INTERNAL:
		code = connect.CodeInternal
	default:
		// Anything else is a hub bug surfacing as a terminal close;
		// stay internal rather than invent a mapping.
	}
	err := connect.NewError(code, errors.New(detail.GetReason().String()))
	if ed, derr := connect.NewErrorDetail(detail); derr == nil {
		err.AddDetail(ed)
	}
	return err
}

// countAttachClosed counts a finished attach by its reason.
func (f *Fleet) countAttachClosed(ctx context.Context, reason typev1.AttachCloseReason) {
	f.countAttach(ctx, "closed", strings.TrimPrefix(reason.String(), "ATTACH_CLOSE_REASON_"))
}
