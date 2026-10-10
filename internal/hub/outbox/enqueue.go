package outbox

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

var (
	// ErrInvalid: the In is not a command the outbox can hold (see [In]).
	// Nothing is written.
	ErrInvalid = errors.New("outbox: invalid command")
	// ErrMachineUnknown: no machines row has the machine id. Nothing is
	// written.
	ErrMachineUnknown = errors.New("outbox: unknown machine")
)

// maxID is the longest command_id the outbox stores (the 02g install_id
// rule: 1 to 64 printable ASCII characters without spaces).
const maxID = 64

// In is one command to put in the outbox.
type In struct {
	// MachineID is the machine the command is for; its machines row must
	// exist.
	MachineID string
	// Command is the agent Command: a kind arm and not_after_time are
	// required; command_id is minted when empty; epoch and seq must be
	// unset, because they are set per connection epoch when it is sent.
	// The caller's message is not changed.
	Command *agentv1.Command
	// RequestID is the API request that caused it, GoalID the goal it
	// belongs to; both optional, for the Operation.
	RequestID, GoalID string
	// Now is the hub's clock when the caller decided: the create time and
	// the stamp's observed_time. not_after_time must be after it.
	Now time.Time
}

// Op is a commands row: the Operation the outbox row is.
type Op struct {
	CommandID, MachineID string
	// Kind is the Command.kind arm name ("start_goal").
	Kind string
	// State is the outbox state (pending, delivered, done, expired,
	// failed); OpState the Operation's (PENDING, DELIVERED, RUNNING, DONE).
	State, OpState string
	// Created is false when the command_id was already stored: Enqueue
	// then changed nothing and returned the stored row.
	Created bool
	// Version and HubSeq are the Operation's version and Watch stamp.
	Version, HubSeq int64
}

// Enqueue puts one command in the outbox inside tx, the writer job of the
// decision that caused it (03f: "commands are enqueued inside the
// resulting tx"), so the decision and its command commit together or not
// at all. It never sends: after the job commits the caller hands the
// returned Op to [Service.Notify]. A command_id already stored is a no-op
// that returns the stored row (Created false). The command's identity is
// its command_id alone; the epoch and per-epoch seq it travels with are
// assigned each time it is sent.
func Enqueue(ctx context.Context, tx *sql.Tx, in In) (Op, error) {
	if err := validate(in); err != nil {
		return Op{}, err
	}
	cmd := proto.CloneOf(in.Command)
	if cmd.GetCommandId().GetValue() == "" {
		id, err := NewCommandID()
		if err != nil {
			return Op{}, err
		}
		cmd.CommandId = &typev1.CommandId{Value: id}
	}
	id := cmd.GetCommandId().GetValue()
	if op, found, err := loadOp(ctx, tx, id); err != nil || found {
		return op, err
	}
	var known int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM machines WHERE machine_id = ?`, in.MachineID).Scan(&known); err != nil {
		return Op{}, fmt.Errorf("outbox: look up machine: %w", err)
	}
	if known == 0 {
		return Op{}, ErrMachineUnknown
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(cmd)
	if err != nil {
		return Op{}, fmt.Errorf("%w: encode: %w", ErrInvalid, err)
	}
	st, err := statedb.NextStamp(ctx, tx, in.Now)
	if err != nil {
		return Op{}, err
	}
	now := in.Now.UnixMilli()
	_, err = tx.ExecContext(ctx, `INSERT INTO commands(command_id, machine_id, kind, command, request_id, goal_id,
		principal_id, create_time, not_after_time, update_time, hub_seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, in.MachineID, KindName(cmd), body, in.RequestID, in.GoalID, cmd.GetRequestedBy().GetId().GetValue(),
		now, cmd.GetNotAfterTime().AsTime().UnixMilli(), now, st.HubSeq)
	if err != nil {
		return Op{}, fmt.Errorf("outbox: insert command: %w", err)
	}
	op, _, err := loadOp(ctx, tx, id)
	op.Created = true
	return op, err
}

func validate(in In) error {
	c := in.Command
	switch {
	case in.MachineID == "":
		return fmt.Errorf("%w: no machine", ErrInvalid)
	case c == nil || c.GetKind() == nil:
		return fmt.Errorf("%w: no command kind", ErrInvalid)
	case in.Now.IsZero():
		return fmt.Errorf("%w: no hub time", ErrInvalid)
	case c.GetNotAfterTime() == nil || !c.GetNotAfterTime().AsTime().After(in.Now):
		return fmt.Errorf("%w: not_after_time must be after now", ErrInvalid)
	case c.GetEpoch() != 0 || c.GetSeq() != 0:
		return fmt.Errorf("%w: epoch and seq are set when the command is sent", ErrInvalid)
	}
	if id := c.GetCommandId().GetValue(); id != "" && !printable(id) {
		return fmt.Errorf("%w: command_id must be 1 to %d printable ASCII characters without spaces", ErrInvalid, maxID)
	}
	return nil
}

// printable is the 02g install_id rule.
func printable(s string) bool {
	if s == "" || len(s) > maxID {
		return false
	}
	for i := range len(s) {
		if s[i] <= ' ' || s[i] > '~' {
			return false
		}
	}
	return true
}

// NewCommandID mints a command_id: 128 random bits as 32 hex characters.
func NewCommandID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("outbox: mint command_id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// KindName is the name of c's kind arm ("send_keys"), the bounded kind
// label of every command metric, or "unknown" when none is set.
func KindName(c *agentv1.Command) string {
	f := c.ProtoReflect().WhichOneof(kindOneof)
	if f == nil {
		return "unknown"
	}
	return string(f.Name())
}

var kindOneof = (&agentv1.Command{}).ProtoReflect().Descriptor().Oneofs().ByName(protoreflect.Name("kind"))

// loadOp reads the row of id; found is false when there is none.
func loadOp(ctx context.Context, q querier, id string) (Op, bool, error) {
	op := Op{CommandID: id}
	err := q.QueryRowContext(ctx, `SELECT machine_id, kind, state, op_state, version, hub_seq FROM commands
		WHERE command_id = ?`, id).Scan(&op.MachineID, &op.Kind, &op.State, &op.OpState, &op.Version, &op.HubSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return Op{}, false, nil
	}
	if err != nil {
		return Op{}, false, fmt.Errorf("outbox: read command: %w", err)
	}
	return op, true, nil
}

// querier is a *sql.Tx of the writer or of the read pool.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
