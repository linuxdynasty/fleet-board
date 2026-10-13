package approvals

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

var (
	// ErrClosed: the approval is no longer PENDING (decided, expired or
	// withdrawn). It wins over ErrVersion, so a client learns "already
	// decided", not "reload" (triage.proto DecideApprovalRequest).
	// A *ClosedError carries which state closed it, read in the decision's
	// own transaction, so the caller never re-reads outside it.
	ErrClosed = errors.New("approvals: approval is closed")
	// ErrVersion: expected_version missed the stored version.
	ErrVersion = errors.New("approvals: version mismatch")
	// ErrRole: the decider's role does not cover required_role.
	ErrRole = errors.New("approvals: role insufficient")
	// ErrDecision: the decision is not one of allowed_decisions.
	ErrDecision = errors.New("approvals: decision not allowed")
	// ErrInflight: another answer for the same prompt is already on its
	// way. The second answer is refused until the result lands (03d W7).
	ErrInflight = errors.New("approvals: answer already in flight")
	// ErrRequestConflict: request_id was used before with another body
	// (02b idempotency).
	ErrRequestConflict = errors.New("approvals: request id reused with another body")
)

// denyKey is the decision key that sends nothing, for every kind: the
// denial leaves the prompt or pane alone (03d "a deny sends nothing").
const denyKey = "deny"

// ClosedError is ErrClosed with the state that closed the approval,
// decided inside the same transaction (no re-read outside it).
type ClosedError struct {
	ID    string
	State hubv1.ApprovalState
}

func (e *ClosedError) Error() string {
	return "approvals: " + e.ID + " is " + e.State.String()
}

// Unwrap reports every ClosedError as ErrClosed.
func (e *ClosedError) Unwrap() error { return ErrClosed }

// Decide records one person's decision inside tx and audits it as
// approval.decide. Order of checks: the replay, the state (ErrClosed wins
// over ErrVersion), the version, the role, then the decision. An approve
// decision for a PROMPT_ANSWER reserves the in-flight row; the caller
// enqueues the command and attaches it with AttachOperation in the same
// transaction. A deny decision sends nothing. Nothing is written on any
// error except the lazy expiry below: deciding a PENDING approval past its
// expire_time expires it first (fallback DENY) and reports ErrClosed.
type DecideInput struct {
	ID              string
	ExpectedVersion uint64
	Decision        string
	Decider         *typev1.Principal
	Actor           statedb.Actor
	RequestID       string
	Now             time.Time
}

// Outcome is what Decide stored.
type Outcome struct {
	Approval *hubv1.Approval
	// Sends is false for a deny: nothing is enqueued.
	Sends bool
	// Replay is true when request_id repeated the stored decision: the
	// stored approval is returned and nothing changes.
	Replay bool
	// PromptID is the answered prompt (PROMPT_ANSWER only), so the caller
	// can build the SendKeys bound to it.
	PromptID string
	// OperationID is the sent command (a replay of a decision that sent),
	// so the caller can return the stored Operation.
	OperationID string
}

// Decide records the decision.
func Decide(ctx context.Context, tx *sql.Tx, in DecideInput) (Outcome, error) {
	if in.RequestID == "" {
		return Outcome{}, fmt.Errorf("%w: request_id is required", ErrInvalid)
	}
	if in.Now.IsZero() {
		return Outcome{}, fmt.Errorf("%w: now is required", ErrInvalid)
	}
	rec, err := getRow(ctx, tx, in.ID)
	if err != nil {
		return Outcome{}, err
	}
	a := rec.approval
	if hit, err := checkReplay(ctx, tx, in, a); err != nil || hit {
		if err != nil {
			return Outcome{}, err
		}
		out := Outcome{Approval: a, Sends: sendsFor(a, in.Decision), Replay: true, OperationID: rec.operation}
		if a.GetKind() == hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER {
			out.PromptID = rec.promptID
		}
		return out, nil
	}
	if a.GetState() != hubv1.ApprovalState_APPROVAL_STATE_PENDING {
		return Outcome{}, &ClosedError{ID: in.ID, State: a.GetState()}
	}
	if !a.GetExpireTime().AsTime().After(in.Now) {
		if err := expireRow(ctx, tx, a, in.Now, "timeout"); err != nil {
			return Outcome{}, err
		}
		return Outcome{}, &ClosedError{ID: in.ID, State: hubv1.ApprovalState_APPROVAL_STATE_EXPIRED}
	}
	if a.GetVersion() != in.ExpectedVersion {
		return Outcome{}, fmt.Errorf("%w: saw %d, stored %d", ErrVersion, in.ExpectedVersion, a.GetVersion())
	}
	if err := checkRole(in.Decider, a.GetRequiredRole()); err != nil {
		return Outcome{}, err
	}
	if !allowed(a, in.Decision) {
		return Outcome{}, fmt.Errorf("%w: %q", ErrDecision, in.Decision)
	}
	sends := sendsFor(a, in.Decision)
	if sends && a.GetKind() == hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER {
		if err := holdInflight(ctx, tx, a.GetMachine().GetValue(), rec.promptID,
			a.GetId().GetValue(), in.Now); err != nil {
			return Outcome{}, err
		}
	}
	decided, err := storeDecision(ctx, tx, a, in, sends, in.Now)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{Approval: decided, Sends: sends}
	if a.GetKind() == hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER {
		out.PromptID = rec.promptID
	}
	return out, nil
}

// sendsFor is false only for the deny key: a deny sends nothing and
// returns no operation (02m, 03d).
func sendsFor(a *hubv1.Approval, decision string) bool {
	return decision != denyKey
}

// allowed reports whether decision names one of the approval's keys.
func allowed(a *hubv1.Approval, decision string) bool {
	if decision == "" {
		return false
	}
	for _, c := range a.GetAllowedDecisions() {
		if c.GetKey() == decision {
			return true
		}
	}
	return false
}

// checkRole covers required_role with the decider's mapped role (02d).
// Until plan row 12 brings caller scopes, the local user maps to OWNER and
// covers every role; a signed-in user covers OPERATOR; tokens, devices and
// unknown callers decide nothing.
func checkRole(decider *typev1.Principal, required typev1.Role) error {
	if decider.GetId().GetValue() == "" {
		return fmt.Errorf("%w: no decider", ErrRole)
	}
	if decider.GetKind() == typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER {
		return nil
	}
	if required == typev1.Role_ROLE_OWNER {
		return fmt.Errorf("%w: needs OWNER", ErrRole)
	}
	if decider.GetKind() == typev1.PrincipalKind_PRINCIPAL_KIND_USER {
		return nil
	}
	return fmt.Errorf("%w: caller kind decides nothing", ErrRole)
}

// checkReplay returns the stored approval when (principal, request_id)
// repeats the stored decision body, or ErrRequestConflict when the same key
// carries another body (02b idempotency).
func checkReplay(ctx context.Context, tx *sql.Tx, in DecideInput, a *hubv1.Approval) (bool, error) {
	var principal, requestID sql.NullString
	var requestHash []byte
	err := tx.QueryRowContext(ctx, `SELECT decide_principal, decide_request_id, decide_request_hash
		FROM approvals WHERE id = ?`, in.ID).Scan(&principal, &requestID, &requestHash)
	if err != nil {
		return false, fmt.Errorf("approvals: replay check: %w", err)
	}
	if !requestID.Valid || requestID.String == "" {
		return false, nil
	}
	if !principal.Valid || principal.String != in.Decider.GetId().GetValue() || requestID.String != in.RequestID {
		return false, nil
	}
	if hex.EncodeToString(requestHash) != decideHash(in) {
		return false, fmt.Errorf("%w: %s", ErrRequestConflict, in.RequestID)
	}
	return true, nil
}

// decideHash is the idempotency body: what was decided, at which version.
func decideHash(in DecideInput) string {
	sum := sha256.Sum256([]byte(in.ID + "\x00" + in.Decision + "\x00" + uint64String(in.ExpectedVersion)))
	return hex.EncodeToString(sum[:])
}

func uint64String(n uint64) string {
	return fmt.Sprintf("%d", n)
}

// storeDecision writes the DECIDED row, stamps it for Watch and audits
// approval.decide, all in the caller's transaction.
func storeDecision(ctx context.Context, tx *sql.Tx, a *hubv1.Approval, in DecideInput, sends bool, now time.Time) (*hubv1.Approval, error) {
	st, err := statedb.NextStamp(ctx, tx, now)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE approvals SET version = version + 1, state = 'DECIDED',
		decided_by_id = ?, decided_by_kind = ?, decided_by_name = ?, decision = ?, decided_time = ?,
		decide_principal = ?, decide_request_id = ?, decide_request_hash = ?,
		update_time = ?, hub_seq = ?, observed_time = ? WHERE id = ? AND state = 'PENDING'`,
		in.Decider.GetId().GetValue(), kindNumber(in.Decider.GetKind()), in.Decider.GetName(),
		in.Decision, now.UnixMilli(),
		in.Decider.GetId().GetValue(), in.RequestID, mustDecodeHash(decideHash(in)),
		now.UnixMilli(), st.HubSeq, st.ObservedTime.UnixMilli(), in.ID)
	if err != nil {
		return nil, fmt.Errorf("approvals: decide: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("approvals: decide: %w", err)
	} else if n == 0 {
		// Lost the race: read the closer's state in this same job.
		rec, rerr := getRow(ctx, tx, in.ID)
		if rerr != nil {
			return nil, rerr
		}
		return nil, &ClosedError{ID: in.ID, State: rec.approval.GetState()}
	}
	detail, err := json.Marshal(map[string]any{"decision": in.Decision, "sends": sends})
	if err != nil {
		return nil, fmt.Errorf("approvals: decide detail: %w", err)
	}
	if _, err := statedb.AppendAudit(ctx, tx, statedb.AuditEntry{
		Actor: in.Actor, Action: "approval.decide", Target: "approvals/" + in.ID,
		Detail: string(detail), Now: now,
	}); err != nil {
		return nil, err
	}
	return Get(ctx, tx, in.ID, now)
}

// promptKey is the in-flight key: one answer per prompt per machine (03d:
// fingerprints include the machine id).
func promptKey(machineID, promptID string) string {
	return machineID + "\x00" + promptID
}

// holdInflight reserves the prompt's answer row. A held row refuses the
// second answer until the result clears it.
func holdInflight(ctx context.Context, tx *sql.Tx, machineID, promptID, approvalID string, now time.Time) error {
	if promptID == "" {
		return fmt.Errorf("%w: prompt approval needs its prompt id", ErrInvalid)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO approval_inflight(prompt_key, approval_id, held_time)
		VALUES (?, ?, ?)`, promptKey(machineID, promptID), approvalID, now.UnixMilli())
	if err != nil {
		if isInflightConflict(err) {
			return fmt.Errorf("%w: %s", ErrInflight, promptID)
		}
		return fmt.Errorf("approvals: hold in-flight: %w", err)
	}
	return nil
}

// ClearInflight drops the prompt's answer row when its CommandResult lands,
// so the next answer for that prompt can go. A missing row is fine.
func ClearInflight(ctx context.Context, tx *sql.Tx, machineID, promptID string) error {
	if machineID == "" || promptID == "" {
		return fmt.Errorf("%w: machine and prompt are required", ErrInvalid)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM approval_inflight WHERE prompt_key = ?`,
		promptKey(machineID, promptID)); err != nil {
		return fmt.Errorf("approvals: clear in-flight: %w", err)
	}
	return nil
}

// isInflightConflict reports the one-answer-per-prompt unique violation.
func isInflightConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: approval_inflight.prompt_key")
}

// mustDecodeHash decodes a hex hash the package encoded itself.
func mustDecodeHash(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("approvals: bad hash: " + err.Error())
	}
	return b
}
