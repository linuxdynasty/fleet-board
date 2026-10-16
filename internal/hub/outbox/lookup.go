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
		row := tx.QueryRowContext(ctx, `SELECT command_id, machine_id, kind, state, op_state, version,
			create_time, update_time, not_after_time, request_id, goal_id, principal_id,
			step, step_index, step_count, message, status, reason,
			ack_time FROM commands WHERE command_id = ?`, id)
		if err := row.Scan(&d.CommandID, &d.MachineID, &d.Kind, &d.State, &d.OpState, &d.Version,
			&created, &updated, &notAfter, &d.RequestID, &d.GoalID, &d.PrincipalID,
			&d.Step, &d.StepIndex, &d.StepCount, &d.Message, &d.Status, &d.Reason,
			&ack); err != nil {
			return err
		}
		d.Created, d.Updated, d.NotAfter = time.UnixMilli(created), time.UnixMilli(updated), time.UnixMilli(notAfter)
		d.HasAck = ack.Valid
		if ack.Valid {
			d.AckTime = time.UnixMilli(ack.Int64)
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
