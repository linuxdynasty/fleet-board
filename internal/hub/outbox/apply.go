package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// From is the stream an agent message arrived on.
type From struct {
	// MachineID is the stream's machine, from its verified certificate.
	MachineID string
	// Epoch is the stream's connection epoch.
	Epoch uint64
	// Live is whether the stream is still its machine's live one. A
	// displaced stream's queued messages still reach the handler table;
	// they are refused (stale_stream) and the agent sends them again on
	// its live stream (a result after the next Snapshot, an ack when the
	// command is re-sent there).
	Live bool
}

// Outcome is what applying one agent message did.
type Outcome int

const (
	// Applied: the commands row changed (and, for a result, the result was
	// stored). The writer job committed.
	Applied Outcome = iota + 1
	// Known: a repeat for a command that already moved past it; nothing
	// changed. A known result is answered with ResultAck like a new one.
	Known
	// Refused: no command of this stream's machine has the id, or the
	// stream is not live. Nothing was written, and it is never answered.
	Refused
)

// Ack applies a CommandAck: pending -> delivered, Operation DELIVERED
// (02i "Command lifecycle").
func (s *Service) Ack(ctx context.Context, from From, a *agentv1.CommandAck) (Outcome, error) {
	now := s.now().UTC()
	out, _, err := s.apply(ctx, from, "command_ack", a.GetCommandId().GetValue(), func(jctx context.Context, tx *sql.Tx, c current) (bool, error) {
		if c.state != "pending" {
			return false, nil
		}
		return true, c.update(jctx, tx, now, `UPDATE commands SET state = 'delivered', op_state = 'DELIVERED', ack_time = ?,
			`+stamped, now.UnixMilli())
	})
	if out == Applied {
		s.log.InfoContext(ctx, "outbox.acked", "command_id", a.GetCommandId().GetValue(), "machine_id", from.MachineID,
			"epoch", from.Epoch)
		s.kick(from.MachineID) // the stream took a command: room for the next
	}
	return out, err
}

// Progress applies a CommandProgress: the Operation is RUNNING at the step
// reported, with its scrubbed message. The outbox row counts as delivered
// (the agent persisted the command before it ran it), even if its ack was
// lost. A closed command ignores it.
func (s *Service) Progress(ctx context.Context, from From, p *agentv1.CommandProgress) (Outcome, error) {
	msg := cleanMessage(p.GetMessage(), MaxMessage)
	s.m.text(ctx, "command_progress.message", msg)
	step := cleanName(p.GetStep())
	now := s.now().UTC()
	out, _, err := s.apply(ctx, from, "command_progress", p.GetCommandId().GetValue(), func(jctx context.Context, tx *sql.Tx, c current) (bool, error) {
		if c.closed() {
			return false, nil
		}
		return true, c.update(jctx, tx, now, `UPDATE commands SET state = 'delivered', op_state = 'RUNNING', step = ?,
			step_index = ?, step_count = ?, message = ?, `+stamped, step, p.GetStepIndex(), p.GetStepCount(), msg.text)
	})
	return out, err
}

// Result applies a CommandResult in one job: the result, its message
// scrubbed, is stored once in command_results, and the row is closed
// (done, Operation DONE with the status and reason). A pending row closes
// too: EPOCH_MISMATCH and SEQ_REPLAYED come without an ack (row 6 ruling).
// A command already closed keeps its first result (Known). The caller
// sends ResultAck after an Applied or Known outcome, never before.
func (s *Service) Result(ctx context.Context, from From, r *agentv1.CommandResult) (Outcome, error) {
	id := r.GetCommandId().GetValue()
	ctx, span := s.tracer.Start(ctx, "hub.command.result", trace.WithAttributes(
		attribute.String("command_id", id), attribute.String("machine_id", from.MachineID),
		attribute.String("status", statusName(r.GetStatus()))), trace.WithLinks(agentLink(ctx, r.GetTrace())))
	defer span.End()
	msg := cleanMessage(r.GetMessage(), MaxMessage)
	s.m.text(ctx, "command_result.message", msg)
	stored := proto.CloneOf(r)
	stored.Message = msg.text
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(stored)
	if err != nil {
		return Refused, fmt.Errorf("outbox: encode result: %w", err)
	}
	status, reason := statusName(r.GetStatus()), reasonName(r.GetReason())
	now := s.now().UTC()
	out, c, err := s.apply(ctx, from, "command_result", id, func(jctx context.Context, tx *sql.Tx, c current) (bool, error) {
		if c.closed() || c.hasResult {
			return false, nil
		}
		if _, err := tx.ExecContext(jctx, `INSERT INTO command_results(command_id, machine_id, epoch, status, reason,
			message, result, received_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, from.MachineID, from.Epoch, status, reason, msg.text, body, now.UnixMilli()); err != nil {
			return false, fmt.Errorf("outbox: store result: %w", err)
		}
		return true, c.update(jctx, tx, now, `UPDATE commands SET state = 'done', op_state = 'DONE', status = ?, reason = ?,
			message = ?, done_time = ?, `+stamped, status, reason, msg.text, now.UnixMilli())
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "apply")
	}
	if out != Applied {
		return out, err
	}
	took := now.Sub(c.created)
	kind := attribute.String("kind", c.kind)
	s.m.commands.Add(ctx, 1, metric.WithAttributes(kind, attribute.String("status", status)))
	s.m.roundtrip.Record(ctx, took.Seconds(), metric.WithAttributes(kind))
	s.log.InfoContext(ctx, "outbox.resulted", "command_id", id, "machine_id", from.MachineID, "epoch", from.Epoch,
		"kind", c.kind, "status", status, "reason", reason, "roundtrip_ms", took.Milliseconds())
	return out, nil
}

// current is the commands row a message is for, read inside its job.
type current struct {
	id, machine, kind, state string
	created                  time.Time
	hasResult                bool
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

// apply runs change on id's row in one writer job, after the checks every
// arm shares; change reports whether it changed the row. It counts and
// logs the outcome.
func (s *Service) apply(ctx context.Context, from From, arm, id string,
	change func(context.Context, *sql.Tx, current) (bool, error),
) (Outcome, current, error) {
	cause := ""
	switch {
	case id == "":
		cause = "invalid"
	case !from.Live:
		cause = "stale_stream"
	}
	var c current
	changed := false
	if cause == "" {
		jctx := context.WithoutCancel(ctx) // see statedb.WriteAudit
		err := s.store.Write(ctx, func(tx *sql.Tx) error {
			cause, changed = "", false
			var err error
			c, cause, err = load(jctx, tx, id, from.MachineID)
			if err != nil || cause != "" {
				return err
			}
			changed, err = change(jctx, tx, c)
			return err
		})
		if err != nil {
			s.m.message(ctx, arm, "failed")
			s.log.ErrorContext(ctx, "outbox.failed", "step", arm, "err_kind", errKind(err), "command_id", id,
				"machine_id", from.MachineID)
			return Refused, c, err
		}
	}
	switch {
	case cause != "":
		s.m.message(ctx, arm, cause)
		if arm == "command_result" && cause == "unknown_command" {
			s.m.unknown.Add(ctx, 1)
		}
		s.log.WarnContext(ctx, "outbox.refused", "arm", arm, "cause", cause, "command_id", id,
			"machine_id", from.MachineID, "epoch", from.Epoch)
		return Refused, c, nil
	case changed:
		s.m.message(ctx, arm, "applied")
		return Applied, c, nil
	default:
		s.m.message(ctx, arm, "known")
		return Known, c, nil
	}
}

// load reads id's row for machine; cause is unknown_command or
// wrong_machine when the message is not for it.
func load(ctx context.Context, tx *sql.Tx, id, machine string) (current, string, error) {
	c := current{id: id}
	var created int64
	err := tx.QueryRowContext(ctx, `SELECT machine_id, kind, state, create_time,
		EXISTS (SELECT 1 FROM command_results r WHERE r.command_id = c.command_id)
		FROM commands c WHERE command_id = ?`, id).Scan(&c.machine, &c.kind, &c.state, &created, &c.hasResult)
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

func statusName(st typev1.Status) string { return strings.TrimPrefix(st.String(), "STATUS_") }

func reasonName(r typev1.ReasonCode) string {
	if r == typev1.ReasonCode_REASON_CODE_UNSPECIFIED {
		return ""
	}
	return strings.TrimPrefix(r.String(), "REASON_CODE_")
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
