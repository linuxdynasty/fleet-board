package approvals

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// MaxPreview is the longest Approval.preview the store keeps (triage.proto:
// 2 KiB of prompt text or exact command).
const MaxPreview = 2048

var (
	// ErrInvalid: the create or decide input is missing a field or breaks a
	// rule. Nothing is written.
	ErrInvalid = errors.New("approvals: invalid input")
	// ErrUnknown: no approval has the id. Nothing is written.
	ErrUnknown = errors.New("approvals: unknown approval")
	// ErrConflict: the id is already stored. Nothing is written.
	ErrConflict = errors.New("approvals: approval id already stored")
)

// Choice is one allowed decision (triage.proto ApprovalChoice).
type Choice struct {
	Key         string
	Label       string
	Destructive bool
}

// Create is one approval to store. The caller is the writer that raised the
// cause (a session-control RPC, ingest for prompts, StartGoal for
// dispatches): creation shares that writer job's transaction, so the cause
// and its approval commit together or not at all.
type Create struct {
	// ID is empty to mint one ("appr_" plus 16 hex digits).
	ID           string
	Kind         hubv1.ApprovalKind
	Severity     hubv1.Severity
	Preview      string
	MachineID    string
	SessionID    string
	Pane         *typev1.PaneKey
	GoalID       string
	TriageItemID string
	Allowed      []Choice
	// PromptID is the agent-minted prompt this answers (PROMPT_ANSWER
	// only): it keys the one in-flight answer per prompt per machine.
	PromptID string
	// Command is the gated agent command (COMMAND_CONFIRM only), sent when
	// the approval is approved. Stored marshaled; nothing is sent before.
	Command *agentv1.Command
	// DefaultChoice is preselected ("deny" for destructive ones).
	DefaultChoice string
	Fallback      hubv1.ApprovalFallback
	ExpireTime    time.Time
	RequiredRole  typev1.Role
	// Now is the hub's clock when the caller decided.
	Now time.Time
}

// Filter lists approvals by state and kind, newest first.
type Filter struct {
	State    hubv1.ApprovalState
	HasState bool
	Kind     hubv1.ApprovalKind
	HasKind  bool
	PageSize int
	// PageToken is the offset; empty starts at 0. Opaque to callers.
	PageToken string
}

// kindName is the store spelling of an ApprovalKind.
func kindName(k hubv1.ApprovalKind) string {
	switch k {
	case hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER:
		return "PROMPT_ANSWER"
	case hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM:
		return "COMMAND_CONFIRM"
	case hubv1.ApprovalKind_APPROVAL_KIND_DISPATCH_CONFIRM:
		return "DISPATCH_CONFIRM"
	default:
		return ""
	}
}

// stateName is the store spelling of an ApprovalState.
func stateName(s hubv1.ApprovalState) string {
	switch s {
	case hubv1.ApprovalState_APPROVAL_STATE_PENDING:
		return "PENDING"
	case hubv1.ApprovalState_APPROVAL_STATE_DECIDED:
		return "DECIDED"
	case hubv1.ApprovalState_APPROVAL_STATE_EXPIRED:
		return "EXPIRED"
	case hubv1.ApprovalState_APPROVAL_STATE_WITHDRAWN:
		return "WITHDRAWN"
	default:
		return ""
	}
}

// mintID is a hub-assigned approval id (common.proto: hub-assigned).
func mintID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("approvals: mint id: %w", err)
	}
	return "appr_" + hex.EncodeToString(b[:]), nil
}

// CreateApproval stores one PENDING approval with version 1 inside tx, the
// raising writer job's transaction, and stamps its hub_seq for Watch.
func CreateApproval(ctx context.Context, tx *sql.Tx, in Create) (*hubv1.Approval, error) {
	if err := checkCreate(in); err != nil {
		return nil, err
	}
	id := in.ID
	if id == "" {
		var err error
		if id, err = mintID(); err != nil {
			return nil, err
		}
	}
	allowed, err := json.Marshal(in.Allowed)
	if err != nil {
		return nil, fmt.Errorf("%w: encode allowed decisions: %w", ErrInvalid, err)
	}
	var rawCmd []byte
	if in.Command != nil {
		rawCmd, err = proto.MarshalOptions{Deterministic: true}.Marshal(in.Command)
		if err != nil {
			return nil, fmt.Errorf("%w: encode gated command: %w", ErrInvalid, err)
		}
	}
	st, err := statedb.NextStamp(ctx, tx, in.Now)
	if err != nil {
		return nil, err
	}
	a := &hubv1.Approval{
		Id:               &hubv1.ApprovalId{Value: id},
		Version:          1,
		CreateTime:       timestamppb.New(in.Now),
		UpdateTime:       timestamppb.New(in.Now),
		Kind:             in.Kind,
		State:            hubv1.ApprovalState_APPROVAL_STATE_PENDING,
		Severity:         in.Severity,
		Preview:          in.Preview,
		Machine:          &typev1.MachineId{Value: in.MachineID},
		Session:          &typev1.SessionId{Value: in.SessionID},
		Pane:             in.Pane,
		Goal:             &typev1.GoalId{Value: in.GoalID},
		TriageItem:       &hubv1.TriageItemId{Value: in.TriageItemID},
		AllowedDecisions: choicesToProto(in.Allowed),
		DefaultChoice:    in.DefaultChoice,
		Fallback:         in.Fallback,
		ExpireTime:       timestamppb.New(in.ExpireTime),
		RequiredRole:     in.RequiredRole,
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO approvals(id, version, kind, state, severity, preview,
		machine_id, session_id, pane_server, pane_server_start_id, pane_id, goal_id, triage_item_id, prompt_id, command,
		allowed, default_choice, fallback, expire_time, required_role,
		create_time, update_time, hub_seq, observed_time)
		VALUES (?, 1, ?, 'PENDING', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, kindName(in.Kind), severityName(in.Severity), in.Preview,
		in.MachineID, in.SessionID, paneServer(in.Pane), paneStartID(in.Pane), paneID(in.Pane),
		in.GoalID, in.TriageItemID, in.PromptID, rawCmd, string(allowed), in.DefaultChoice, fallbackName(in.Fallback),
		in.ExpireTime.UnixMilli(), roleName(in.RequiredRole),
		in.Now.UnixMilli(), in.Now.UnixMilli(), st.HubSeq, st.ObservedTime.UnixMilli())
	if err != nil {
		if isConflict(err) {
			return nil, fmt.Errorf("%w: %s", ErrConflict, id)
		}
		return nil, fmt.Errorf("approvals: insert: %w", err)
	}
	return a, nil
}

func checkCreate(in Create) error {
	switch {
	case kindName(in.Kind) == "":
		return fmt.Errorf("%w: kind is required", ErrInvalid)
	case in.Fallback != hubv1.ApprovalFallback_APPROVAL_FALLBACK_DENY:
		return fmt.Errorf("%w: v1 only falls back to DENY", ErrInvalid)
	case len(in.Preview) > MaxPreview:
		return fmt.Errorf("%w: preview is %d bytes, over %d", ErrInvalid, len(in.Preview), MaxPreview)
	case in.Kind == hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER && in.PromptID == "":
		return fmt.Errorf("%w: a prompt answer needs its prompt id", ErrInvalid)
	case in.Kind != hubv1.ApprovalKind_APPROVAL_KIND_PROMPT_ANSWER && in.PromptID != "":
		return fmt.Errorf("%w: only a prompt answer carries a prompt id", ErrInvalid)
	case in.Kind == hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM && in.Command == nil:
		return fmt.Errorf("%w: a command confirm stores its gated command", ErrInvalid)
	case in.Kind != hubv1.ApprovalKind_APPROVAL_KIND_COMMAND_CONFIRM && in.Command != nil:
		return fmt.Errorf("%w: only a command confirm stores a command", ErrInvalid)
	case in.ExpireTime.IsZero():
		return fmt.Errorf("%w: expire_time is required", ErrInvalid)
	case roleName(in.RequiredRole) == "":
		return fmt.Errorf("%w: required_role is required", ErrInvalid)
	case in.Now.IsZero():
		return fmt.Errorf("%w: now is required", ErrInvalid)
	case !in.ExpireTime.After(in.Now):
		return fmt.Errorf("%w: expire_time must be after now", ErrInvalid)
	}
	return nil
}

// rowColumns lists every approvals column scan reads, in scan order.
const rowColumns = `id, version, kind, state, severity, preview,
		machine_id, session_id, pane_server, pane_server_start_id, pane_id, goal_id, triage_item_id, prompt_id, operation,
		allowed, default_choice, fallback, expire_time, required_role,
		decided_by_id, decided_by_kind, decided_by_name, decision, decided_time,
		create_time, update_time`

// Get reads one approval by id. A PENDING approval past its expire_time is
// expired in the same transaction (fallback DENY, audited), so a client
// that polls the approval sees EXPIRED without waiting for the
// housekeeping pass; a zero now skips the lazy step.
func Get(ctx context.Context, tx *sql.Tx, id string, now time.Time) (*hubv1.Approval, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: id is required", ErrInvalid)
	}
	rec, err := getRow(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	a := rec.approval
	if !now.IsZero() && a.GetState() == hubv1.ApprovalState_APPROVAL_STATE_PENDING &&
		!a.GetExpireTime().AsTime().After(now) {
		if err := expireRow(ctx, tx, a, now, "timeout"); err != nil {
			return nil, err
		}
		rec, err = getRow(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		a = rec.approval
	}
	return a, nil
}

func getRow(ctx context.Context, tx *sql.Tx, id string) (record, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+rowColumns+` FROM approvals WHERE id = ?`, id)
	rec, err := scan(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rec, fmt.Errorf("%w: %s", ErrUnknown, id)
		}
		return rec, err
	}
	return rec, nil
}

// List reads approvals newest first. PageSize defaults to 50 and caps at
// 200 (02b paging); PageToken is the row offset. One row past the page is
// read to detect the next page: the returned token is empty on the last
// page and the offset of the next page otherwise.
func List(ctx context.Context, tx *sql.Tx, f Filter) ([]*hubv1.Approval, string, error) {
	size := f.PageSize
	if size <= 0 {
		size = 50
	}
	if size > 200 {
		size = 200
	}
	offset := 0
	if f.PageToken != "" {
		var err error
		if offset, err = parseOffset(f.PageToken); err != nil {
			return nil, "", err
		}
	}
	q := `SELECT ` + rowColumns + ` FROM approvals WHERE 1 = 1`
	var args []any
	if f.HasState {
		name := stateName(f.State)
		if name == "" {
			return nil, "", fmt.Errorf("%w: bad state filter", ErrInvalid)
		}
		q += ` AND state = ?`
		args = append(args, name)
	}
	if f.HasKind {
		name := kindName(f.Kind)
		if name == "" {
			return nil, "", fmt.Errorf("%w: bad kind filter", ErrInvalid)
		}
		q += ` AND kind = ?`
		args = append(args, name)
	}
	q += ` ORDER BY create_time DESC, id LIMIT ? OFFSET ?`
	args = append(args, size+1, offset)
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("approvals: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*hubv1.Approval
	for rows.Next() {
		rec, err := scan(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, rec.approval)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("approvals: list: %w", err)
	}
	if len(out) <= size {
		return out, "", nil
	}
	return out[:size], offsetString(offset + size), nil
}

// offsetString is the opaque next-page token: the next row offset.
func offsetString(offset int) string {
	return fmt.Sprintf("%d", offset)
}
