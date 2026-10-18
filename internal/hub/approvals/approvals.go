package approvals

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	// Checkpoint runs after input/encoding checks, before the stamp and
	// row insert. It must not access the store or network.
	Checkpoint func() error
}

// Filter lists approvals by state and kind, newest first.
type Filter struct {
	State    hubv1.ApprovalState
	HasState bool
	Kind     hubv1.ApprovalKind
	HasKind  bool
	PageSize int
	// PageToken is an opaque cursor; empty starts the first page.
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
	if in.Checkpoint != nil {
		if err := in.Checkpoint(); err != nil {
			return nil, err
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
		Pane:             in.Pane,
		AllowedDecisions: choicesToProto(in.Allowed),
		DefaultChoice:    in.DefaultChoice,
		Fallback:         in.Fallback,
		ExpireTime:       timestamppb.New(in.ExpireTime),
		RequiredRole:     in.RequiredRole,
	}
	// Message fields stay absent when the create holds nothing for them,
	// like the scan read-back does.
	if in.MachineID != "" {
		a.Machine = &typev1.MachineId{Value: in.MachineID}
	}
	if in.SessionID != "" {
		a.Session = &typev1.SessionId{Value: in.SessionID}
	}
	if in.GoalID != "" {
		a.Goal = &typev1.GoalId{Value: in.GoalID}
	}
	if in.TriageItemID != "" {
		a.TriageItem = &hubv1.TriageItemId{Value: in.TriageItemID}
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

// pageTokenLife is how long a List page token stays valid (02b paging:
// opaque, valid for 10 min, from the same query).
const pageTokenLife = 10 * time.Minute

// List reads approvals newest first. PageSize defaults to 50 and caps at
// 500 (02b paging). PageToken is an opaque cursor: the query it came from
// plus the last row seen. One row past the page is read to detect the
// next page: the returned token is empty on the last page. A cursor stays
// valid while rows only arrive in front of it, so inserts between pages
// neither repeat nor skip rows.
func List(ctx context.Context, tx *sql.Tx, f Filter, now time.Time) ([]*hubv1.Approval, string, error) {
	in, err := parseListInput(f, now)
	if err != nil {
		return nil, "", err
	}
	q := `SELECT ` + rowColumns + ` FROM approvals WHERE 1 = 1`
	var args []any
	if f.HasState {
		q += ` AND state = ?`
		args = append(args, in.state)
	}
	if f.HasKind {
		q += ` AND kind = ?`
		args = append(args, in.kind)
	}
	if in.cursor != nil {
		q += ` AND (create_time < ? OR (create_time = ? AND id < ?))`
		args = append(args, in.cursor.CreateMillis, in.cursor.CreateMillis, in.cursor.ID)
	}
	q += ` ORDER BY create_time DESC, id DESC LIMIT ?`
	args = append(args, in.size+1)
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
	if len(out) <= in.size {
		return out, "", nil
	}
	last := out[in.size-1]
	return out[:in.size], mintToken(in.want, last.GetCreateTime().AsTime(), last.GetId().GetValue(), now), nil
}

// queryHash binds a page token to the query that minted it: state, kind
// and page size. A token from another query is rejected.
func queryHash(f Filter, size int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%s\x00%d",
		stateName(f.State), kindName(f.Kind), size))
	return hex.EncodeToString(sum[:])
}

// pageCursor is a decoded List page token.
type pageCursor struct {
	CreateMillis int64
	ID           string
}

// pageToken is the minted form: version, query hash, cursor, expiry.
type pageToken struct {
	V   int    `json:"v"`
	Q   string `json:"q"`
	CT  int64  `json:"ct"`
	ID  string `json:"id"`
	Exp int64  `json:"exp"`
}

// mintToken seals a next-page token: clients treat it as opaque.
func mintToken(query string, created time.Time, id string, now time.Time) string {
	raw, err := json.Marshal(pageToken{
		V: 1, Q: query,
		CT: created.UnixMilli(), ID: id, Exp: now.Add(pageTokenLife).Unix(),
	})
	if err != nil {
		return ""
	}
	return "v1." + base64.RawURLEncoding.EncodeToString(raw)
}

// parseToken opens a List page token minted for want by now: a token from
// another query, a past one, or a garbled one is ErrInvalid.
func parseToken(tok, want string, now time.Time) (*pageCursor, error) {
	rest, ok := strings.CutPrefix(tok, "v1.")
	if !ok {
		return nil, fmt.Errorf("%w: bad page_token", ErrInvalid)
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil {
		return nil, fmt.Errorf("%w: bad page_token", ErrInvalid)
	}
	var t pageToken
	if err := json.Unmarshal(raw, &t); err != nil || t.V != 1 {
		return nil, fmt.Errorf("%w: bad page_token", ErrInvalid)
	}
	if t.Q != want || t.ID == "" || now.Unix() > t.Exp {
		return nil, fmt.Errorf("%w: bad page_token", ErrInvalid)
	}
	return &pageCursor{CreateMillis: t.CT, ID: t.ID}, nil
}
