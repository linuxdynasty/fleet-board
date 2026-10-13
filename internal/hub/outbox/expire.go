package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Why a command was closed by the hub rather than by its result.
const (
	// CauseDeadline: its not_after_time came with no result (02i: pending
	// or delivered --> expired).
	CauseDeadline = "deadline"
	// CauseRevoked: its machine was revoked (02g "Revoke, in one
	// transaction": pending or delivered --> done, status EXPIRED).
	CauseRevoked = "revoked"
)

// expireBatch bounds the rows one housekeeping job expires, so a long
// outage of many machines never makes one long writer job.
const expireBatch = 500

// Closed is one open command the hub closed without a result.
type Closed struct {
	CommandID, MachineID, Kind string
	// From is the outbox state it left: pending or delivered.
	From string
	// Cause is [CauseDeadline] or [CauseRevoked].
	Cause string
	// Created is its enqueue time, At the hub clock that closed it.
	Created, At time.Time
}

// Expire closes, inside tx (the caller's writer job), up to limit open
// commands (pending or delivered) whose not_after_time is at or before
// now: of machineID, or of every machine when it is "". Each becomes
// expired, Operation DONE with status and reason EXPIRED and done_time
// now, with its own version and hub_seq. It never sends; after the job
// commits the caller hands the result to [Service.Closed]. Oldest
// deadline first.
func Expire(ctx context.Context, tx *sql.Tx, machineID string, now time.Time, limit int) ([]Closed, error) {
	return closeOpen(ctx, tx, `SELECT command_id, machine_id, kind, state, create_time FROM commands
		WHERE state IN ('pending', 'delivered') AND not_after_time <= ? AND (? = '' OR machine_id = ?)
		ORDER BY not_after_time, id LIMIT ?`, []any{now.UnixMilli(), machineID, machineID, limit},
		now, "expired", "EXPIRED", CauseDeadline)
}

// CloseForRevoke closes, inside the revoke's writer job, every open
// command of machineID whatever its deadline: done, Operation DONE,
// status EXPIRED, reason MACHINE_REVOKED, so nothing is sent to it again
// (02g "Revoke, in one transaction", 02i). RevokeMachine (plan row 13a)
// calls it; after the commit the caller hands the result to
// [Service.Closed].
func CloseForRevoke(ctx context.Context, tx *sql.Tx, machineID string, now time.Time) ([]Closed, error) {
	return closeOpen(ctx, tx, `SELECT command_id, machine_id, kind, state, create_time FROM commands
		WHERE state IN ('pending', 'delivered') AND machine_id = ? ORDER BY id`, []any{machineID},
		now, "done", "MACHINE_REVOKED", CauseRevoked)
}

// closeOpen reads the open rows query selects, then closes each to state
// with status EXPIRED and reason.
func closeOpen(ctx context.Context, tx *sql.Tx, query string, args []any, now time.Time, state, reason,
	cause string,
) ([]Closed, error) {
	out, err := readOpen(ctx, tx, query, args, now, cause)
	if err != nil {
		return nil, fmt.Errorf("outbox: read open commands: %w", err)
	}
	for _, c := range out {
		row := current{id: c.CommandID}
		if err := row.update(ctx, tx, now, `UPDATE commands SET state = ?, op_state = 'DONE', status = 'EXPIRED',
			reason = ?, done_time = ?, `+stamped, state, reason, now.UnixMilli()); err != nil {
			return nil, err
		}
		// An expired answer never lands: release its prompt so the next
		// answer for it can go (03d "Hub restart").
		if c.Kind == "send_keys" {
			if err := releasePrompt(ctx, tx, c.MachineID, c.CommandID); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// readOpen reads the open rows query selects, closing its cursor before
// any row is changed.
func readOpen(ctx context.Context, tx *sql.Tx, query string, args []any, now time.Time, cause string) ([]Closed, error) {
	q, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = q.Close() }()
	var out []Closed
	for q.Next() {
		c := Closed{Cause: cause, At: now}
		var created int64
		if err := q.Scan(&c.CommandID, &c.MachineID, &c.Kind, &c.From, &created); err != nil {
			return nil, err
		}
		c.Created = time.UnixMilli(created)
		out = append(out, c)
	}
	return out, q.Err()
}

// Closed is what a caller of [Expire] or [CloseForRevoke] does once its
// job committed: each row is logged (outbox.expired) and counted
// (fleet_hub_commands_expired_total{kind, cause}).
func (s *Service) Closed(ctx context.Context, list []Closed) {
	for _, c := range list {
		s.m.expired.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", c.Kind), attribute.String("cause", c.Cause)))
		s.log.InfoContext(ctx, "outbox.expired", "command_id", c.CommandID, "machine_id", c.MachineID, "kind", c.Kind,
			"from", c.From, "cause", c.Cause, "age_ms", c.At.Sub(c.Created).Milliseconds())
	}
}

// ExpireDue is the housekeeping job "outbox_expiry": it expires every
// machine's commands due at now, in writer jobs of at most 500 rows,
// logging each job's rows once it committed.
func (s *Service) ExpireDue(ctx context.Context, now time.Time) error {
	for {
		var list []Closed
		jctx := context.WithoutCancel(ctx)
		err := s.store.Write(ctx, func(tx *sql.Tx) error {
			var err error
			list, err = Expire(jctx, tx, "", now, expireBatch)
			return err
		})
		if err != nil {
			return fmt.Errorf("outbox: expire: %w", err)
		}
		s.Closed(ctx, list)
		if len(list) < expireBatch {
			return nil
		}
	}
}
