package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// ErrCommandUnknown reads a command_id the outbox never stored.
var ErrCommandUnknown = errors.New("outbox: unknown command")

// Detail is one commands row as the hub API reads it: the stored half of
// the Operation plus the ack time GetOperation's delivered_time comes
// from. HasAck is false until the agent's ack lands, so an expiry before
// delivery reads apart from one after.
type Detail struct {
	CommandID, MachineID, Kind, State, OpState string
	Version                                    int64
	Created, Updated, NotAfter                 time.Time
	RequestID, GoalID, PrincipalID             string
	Step                                       string
	StepIndex, StepCount                       int64
	Message, Status, Reason                    string
	AckTime                                    time.Time
	HasAck                                     bool
	// Command is the stored agent command: its RequestedBy is the full
	// requesting principal. Result is the first stored CommandResult,
	// present exactly when HasResult is true.
	Command   []byte
	Result    []byte
	HasResult bool
}

// Lookup reads id's commands row on the read pool: a short read
// transaction, never the writer, and never across a network call. The API
// caller is a local user, not the command's machine, so there is no
// machine check.
func Lookup(ctx context.Context, db *store.Store, id string) (Detail, error) {
	var d Detail
	err := db.Read(ctx, func(tx *sql.Tx) error {
		var created, updated, notAfter int64
		var ack sql.NullInt64
		var result sql.Null[[]byte]
		row := tx.QueryRowContext(ctx, `SELECT c.command_id, c.machine_id, c.kind, c.state, c.op_state, c.version,
			c.create_time, c.update_time, c.not_after_time, c.request_id, c.goal_id, c.principal_id,
			c.step, c.step_index, c.step_count, c.message, c.status, c.reason,
			c.ack_time, c.command, r.result FROM commands c
			LEFT JOIN command_results r ON r.command_id = c.command_id WHERE c.command_id = ?`, id)
		if err := row.Scan(&d.CommandID, &d.MachineID, &d.Kind, &d.State, &d.OpState, &d.Version,
			&created, &updated, &notAfter, &d.RequestID, &d.GoalID, &d.PrincipalID,
			&d.Step, &d.StepIndex, &d.StepCount, &d.Message, &d.Status, &d.Reason,
			&ack, &d.Command, &result); err != nil {
			return err
		}
		d.Created, d.Updated, d.NotAfter = time.UnixMilli(created), time.UnixMilli(updated), time.UnixMilli(notAfter)
		d.HasAck = ack.Valid
		if ack.Valid {
			d.AckTime = time.UnixMilli(ack.Int64)
		}
		d.HasResult = result.Valid
		if result.Valid {
			d.Result = result.V
		}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, ErrCommandUnknown
	}
	if err != nil {
		return Detail{}, fmt.Errorf("outbox: read command: %w", err)
	}
	return d, nil
}
