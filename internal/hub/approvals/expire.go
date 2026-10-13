package approvals

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// ExpirePass is the housekeeping entry: one writer job that expires every
// past-due PENDING approval (fallback DENY, audited as approval.expire).
// Decide and Get expire their own row lazily, so a client that polls an
// approval sees EXPIRED even between passes.
func ExpirePass(ctx context.Context, s *store.Store, now time.Time) ([]string, error) {
	var expired []string
	err := s.Write(ctx, func(tx *sql.Tx) error {
		var err error
		expired, err = ExpireDue(ctx, tx, now, 200)
		return err
	})
	return expired, err
}

// ExpireDue moves every PENDING approval past its expire_time to EXPIRED
// and returns the ids it expired.
func ExpireDue(ctx context.Context, tx *sql.Tx, now time.Time, limit int) ([]string, error) {
	if now.IsZero() {
		return nil, fmt.Errorf("%w: now is required", ErrInvalid)
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM approvals
		WHERE state = 'PENDING' AND expire_time <= ? ORDER BY expire_time LIMIT ?`, now.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("approvals: expire scan: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("approvals: expire scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("approvals: expire scan: %w", err)
	}
	// Get expires each due row lazily (fallback DENY, audited), so driving
	// Get over the due ids is the whole pass.
	for _, id := range ids {
		if _, err := Get(ctx, tx, id, now); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// expireRow moves a PENDING approval to EXPIRED with the DENY fallback and
// audits it. Nothing is ever sent for an expired approval (02m, 03d).
func expireRow(ctx context.Context, tx *sql.Tx, a *hubv1.Approval, now time.Time, cause string) error {
	st, err := statedb.NextStamp(ctx, tx, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE approvals SET version = version + 1, state = 'EXPIRED',
		update_time = ?, hub_seq = ?, observed_time = ? WHERE id = ? AND state = 'PENDING'`,
		now.UnixMilli(), st.HubSeq, st.ObservedTime.UnixMilli(), a.GetId().GetValue())
	if err != nil {
		return fmt.Errorf("approvals: expire: %w", err)
	}
	detail := `{"fallback":"deny","cause":` + jsonString(cause) + `}`
	_, err = statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
		Action: "approval.expire", Target: "approvals/" + a.GetId().GetValue(),
		Detail: detail, Now: now,
	})
	return err
}

// Withdraw retires a PENDING prompt approval whose prompt left the screen
// (PromptGone, pane exit): it becomes WITHDRAWN, audited as
// approval.withdraw, and a later DecideApproval for it fails with PROMPT_GONE
// at the API layer. Row 5's ingest calls it from the PromptGone event's
// transaction. Withdrawing anything but a PENDING prompt approval is ErrInvalid.
func Withdraw(ctx context.Context, tx *sql.Tx, id string, actor statedb.Actor, now time.Time) (*hubv1.Approval, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	if now.IsZero() {
		return nil, fmt.Errorf("%w: now is required", ErrInvalid)
	}
	a, err := Get(ctx, tx, id, now)
	if err != nil {
		return nil, err
	}
	if a.GetState() != hubv1.ApprovalState_APPROVAL_STATE_PENDING {
		return nil, fmt.Errorf("%w: %s is %v", ErrClosed, id, a.GetState())
	}
	if a.GetKind() != hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER {
		return nil, fmt.Errorf("%w: only a prompt approval withdraws", ErrInvalid)
	}
	st, err := statedb.NextStamp(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE approvals SET version = version + 1, state = 'WITHDRAWN',
		update_time = ?, hub_seq = ?, observed_time = ? WHERE id = ? AND state = 'PENDING'`,
		now.UnixMilli(), st.HubSeq, st.ObservedTime.UnixMilli(), id)
	if err != nil {
		return nil, fmt.Errorf("approvals: withdraw: %w", err)
	}
	if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
		Actor: actor, Action: "approval.withdraw", Target: "approvals/" + id,
		Detail: `{"cause":"prompt_gone"}`, Now: now,
	}); err != nil {
		return nil, err
	}
	return Get(ctx, tx, id, now)
}

// AttachOperation records the command a decided approval sent (outbox
// command_id). The caller enqueues first with outbox.Enqueue, then attaches
// in the same transaction, so the decision and its command commit together.
func AttachOperation(ctx context.Context, tx *sql.Tx, id, commandID string, now time.Time) error {
	if id == "" || commandID == "" {
		return fmt.Errorf("%w: id and command are required", ErrInvalid)
	}
	if now.IsZero() {
		return fmt.Errorf("%w: now is required", ErrInvalid)
	}
	st, err := statedb.NextStamp(ctx, tx, now)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET operation = ?, update_time = ?,
		hub_seq = ?, observed_time = ? WHERE id = ? AND state = 'DECIDED'`,
		commandID, now.UnixMilli(), st.HubSeq, st.ObservedTime.UnixMilli(), id)
	if err != nil {
		return fmt.Errorf("approvals: attach operation: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("approvals: attach operation: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: %s is not decided", ErrClosed, id)
	}
	return nil
}

// StoredCommand reads the gated command a COMMAND_CONFIRM approval holds.
// DecideApproval sends a clone of it when the approval is approved.
func StoredCommand(ctx context.Context, tx *sql.Tx, id string) (*agentv1.Command, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT command FROM approvals WHERE id = ?`, id).Scan(&raw); err != nil {
		return nil, fmt.Errorf("approvals: stored command: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: %s stores no command", ErrInvalid, id)
	}
	var cmd agentv1.Command
	if err := proto.Unmarshal(raw, &cmd); err != nil {
		return nil, fmt.Errorf("approvals: stored command: %w", err)
	}
	return &cmd, nil
}

// InflightHolder returns the approval holding the prompt's in-flight answer,
// or empty when none is held. Tests and the Triage view use it; the writer
// never reads it (the unique constraint decides).
func InflightHolder(ctx context.Context, tx *sql.Tx, machineID, promptID string) (string, error) {
	var holder string
	err := tx.QueryRowContext(ctx, `SELECT approval_id FROM approval_inflight WHERE prompt_key = ?`,
		promptKey(machineID, promptID)).Scan(&holder)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return holder, err
}
