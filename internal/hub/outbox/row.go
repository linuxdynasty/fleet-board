package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// current is the commands row a message is for.
type current struct {
	id, machine, kind, state, opState, step, message string
	stepIndex, stepCount                             int64
	created                                          time.Time
	hasResult                                        bool
}

func (c current) closed() bool { return c.state != "pending" && c.state != "delivered" }

// stamped ends every Operation update: the version rises, and the
// update time and hub_seq stamp follow, for the row of one command_id.
const stamped = `version = version + 1, update_time = ?, hub_seq = ? WHERE command_id = ?`

// update runs query, an UPDATE of c's row ending in [stamped], with args
// and then the stamp's values, in the job's transaction.
func (c current) update(ctx context.Context, tx *sql.Tx, now time.Time, query string, args ...any) error {
	st, err := statedb.NextStamp(ctx, tx, now)
	if err != nil {
		return err
	}
	args = append(args, now.UnixMilli(), st.HubSeq, c.id)
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("outbox: update command: %w", err)
	}
	return nil
}

// apply runs change on id's row when needs says the row must change,
// after the checks every arm shares, and counts and logs the outcome. It
// decides first on the read pool, so a repeat or a refusal costs no
// writer job; a row that needs the change is loaded and decided again
// inside the job, which alone writes.
func (s *Service) apply(ctx context.Context, from From, arm, id string, needs func(current) bool,
	change func(context.Context, *sql.Tx, current) error,
) (Outcome, current, error) {
	switch {
	case !printable(id):
		s.refused(ctx, from, arm, id, "invalid")
		return Refused, current{}, nil
	case !from.Live:
		s.refused(ctx, from, arm, id, "stale_stream")
		return Refused, current{}, nil
	}
	var c current
	cause, changed := "", false
	err := s.store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		c, cause, err = load(ctx, tx, id, from.MachineID)
		return err
	})
	if err == nil && cause == "" && needs(c) {
		jctx := context.WithoutCancel(ctx) // see statedb.WriteAudit
		err = s.store.Write(ctx, func(tx *sql.Tx) error {
			cause, changed = "", false
			var err error
			c, cause, err = load(jctx, tx, id, from.MachineID)
			if err != nil || cause != "" || !needs(c) {
				return err
			}
			changed = true
			return change(jctx, tx, c)
		})
	}
	switch {
	case err != nil:
		s.m.message(ctx, arm, "failed")
		s.log.ErrorContext(ctx, "outbox.failed", "step", arm, "err_kind", errKind(err), "command_id", id,
			"machine_id", from.MachineID)
		return Refused, c, err
	case cause != "":
		if arm == "command_result" && cause == "unknown_command" {
			s.m.unknown.Add(ctx, 1)
		}
		s.refused(ctx, from, arm, id, cause)
		return Refused, c, nil
	case changed:
		s.m.message(ctx, arm, "applied")
		return Applied, c, nil
	default:
		s.m.message(ctx, arm, "known")
		return Known, c, nil
	}
}

// refused counts and logs a message the hub does not take. A malformed id
// is never logged.
func (s *Service) refused(ctx context.Context, from From, arm, id, cause string) {
	s.m.message(ctx, arm, cause)
	s.log.WarnContext(ctx, "outbox.refused", "arm", arm, "cause", cause, "command_id", boundedID(id),
		"machine_id", from.MachineID, "epoch", from.Epoch)
}

// boundedID is id when it is a well-formed command_id, else "": agent text
// of any size never reaches a log line or a span.
func boundedID(id string) string {
	if printable(id) {
		return id
	}
	return ""
}

// load reads id's row for machine; cause is unknown_command or
// wrong_machine when the message is not for it.
func load(ctx context.Context, tx *sql.Tx, id, machine string) (current, string, error) {
	c := current{id: id}
	var created int64
	err := tx.QueryRowContext(ctx, `SELECT machine_id, kind, state, op_state, step, step_index, step_count, message,
		create_time, EXISTS (SELECT 1 FROM command_results r WHERE r.command_id = c.command_id)
		FROM commands c WHERE command_id = ?`, id).Scan(&c.machine, &c.kind, &c.state, &c.opState, &c.step,
		&c.stepIndex, &c.stepCount, &c.message, &created, &c.hasResult)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return c, "unknown_command", nil
	case err != nil:
		return c, "", fmt.Errorf("outbox: read command: %w", err)
	case c.machine != machine:
		return c, "wrong_machine", nil
	}
	c.created = time.UnixMilli(created)
	return c, "", nil
}

// statusName is st's name without STATUS_, or UNKNOWN for a value this hub
// has no name for: the label and the stored column stay a closed set.
func statusName(st typev1.Status) string {
	if name, ok := typev1.Status_name[int32(st)]; ok {
		return strings.TrimPrefix(name, "STATUS_")
	}
	return "UNKNOWN"
}

// reasonName is r's name without REASON_CODE_, "" for none, or UNKNOWN.
func reasonName(r typev1.ReasonCode) string {
	if r == typev1.ReasonCode_REASON_CODE_UNSPECIFIED {
		return ""
	}
	if name, ok := typev1.ReasonCode_name[int32(r)]; ok {
		return strings.TrimPrefix(name, "REASON_CODE_")
	}
	return "UNKNOWN"
}

// errKind is a bounded class of a store error.
func errKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "io"
	}
}
