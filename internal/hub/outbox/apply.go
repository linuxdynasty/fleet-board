package outbox

import (
	"context"
	"database/sql"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
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
	// Known: a repeat for a command that already moved past it, or that
	// says what the row already holds; nothing changed. A known result is
	// answered with ResultAck like a new one.
	Known
	// Refused: not an answer this hub takes: no command of this stream's
	// machine has the id, the id is malformed, the stream is not live, or
	// it is a refusal the agent did not log. Nothing was written, and it
	// is never answered.
	Refused
)

// Ack applies a CommandAck: pending -> delivered, Operation DELIVERED
// (02i "Command lifecycle").
func (s *Service) Ack(ctx context.Context, from From, a *agentv1.CommandAck) (Outcome, error) {
	id := a.GetCommandId().GetValue()
	now := s.now().UTC()
	out, _, err := s.apply(ctx, from, "command_ack", id, func(c current) bool { return c.state == "pending" },
		func(jctx context.Context, tx *sql.Tx, c current) error {
			return c.update(jctx, tx, now, `UPDATE commands SET state = 'delivered', op_state = 'DELIVERED', ack_time = ?,
				`+stamped, now.UnixMilli())
		})
	if out == Applied {
		s.log.InfoContext(ctx, "outbox.acked", "command_id", id, "machine_id", from.MachineID, "epoch", from.Epoch)
		s.kick(from.MachineID) // the stream took a command: room for the next
	}
	return out, err
}

// Progress applies a CommandProgress: the Operation is RUNNING at the step
// reported, with its scrubbed message. The outbox row counts as delivered
// (the agent persisted the command before it ran it), even if its ack was
// lost. A closed command ignores it, and so does a row that already holds
// this progress: the agent re-sends its last progress after every
// reconnect, and a repeat bumps nothing.
func (s *Service) Progress(ctx context.Context, from From, p *agentv1.CommandProgress) (Outcome, error) {
	msg := cleanMessage(p.GetMessage(), MaxMessage)
	s.m.text(ctx, "command_progress.message", msg)
	step := cleanName(p.GetStep())
	now := s.now().UTC()
	out, _, err := s.apply(ctx, from, "command_progress", p.GetCommandId().GetValue(), func(c current) bool {
		same := c.state == "delivered" && c.opState == "RUNNING" && c.step == step && c.stepIndex == int64(p.GetStepIndex()) &&
			c.stepCount == int64(p.GetStepCount()) && c.message == msg.text
		return !c.closed() && !same
	}, func(jctx context.Context, tx *sql.Tx, c current) error {
		return c.update(jctx, tx, now, `UPDATE commands SET state = 'delivered', op_state = 'RUNNING', step = ?,
			step_index = ?, step_count = ?, message = ?, `+stamped, step, p.GetStepIndex(), p.GetStepCount(), msg.text)
	})
	return out, err
}

// Result applies a CommandResult in one job: the result, its message
// scrubbed and its metadata bounded, is stored once in command_results,
// and the row is closed (done, Operation DONE with the status and reason),
// from pending too (the ack was lost). A command already closed keeps its
// first result (Known). A result for a row the hub closed with no result
// (expired, or closed by a revoke) is Known too and stored nowhere; it is
// logged (outbox.late_result) and counted as outcome late. The caller
// sends ResultAck after an Applied or Known outcome, never before.
//
// A refusal the agent sends without logging the command (EPOCH_MISMATCH,
// SEQ_REPLAYED: it answered a stale copy, row 6 ruling, 02-contracts
// "Envelope checks") is Refused: nothing is stored, the row stays as it
// is and is not answered, so a pending row goes out again under the next
// ready epoch with the same command_id.
// Closing the row on it would answer that re-issue Known forever.
func (s *Service) Result(ctx context.Context, from From, r *agentv1.CommandResult) (Outcome, error) {
	id := r.GetCommandId().GetValue()
	status, reason := statusName(r.GetStatus()), reasonName(r.GetReason())
	ctx, span := s.tracer.Start(ctx, "hub.command.result", trace.WithAttributes(
		attribute.String("command_id", boundedID(id)), attribute.String("machine_id", from.MachineID),
		attribute.String("status", status)), trace.WithLinks(agentLink(ctx, r.GetTrace())))
	defer span.End()
	if cause := unlogged(r.GetReason()); cause != "" {
		s.refused(ctx, from, "command_result", id, cause)
		return Refused, nil
	}
	msg := cleanMessage(r.GetMessage(), MaxMessage)
	s.m.text(ctx, "command_result.message", msg)
	stored := proto.CloneOf(r)
	stored.Message = msg.text
	stored.Metadata = cleanMetadata(r.GetMetadata())
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(stored)
	if err != nil {
		return Refused, fmt.Errorf("outbox: encode result: %w", err)
	}
	now := s.now().UTC()
	out, c, err := s.apply(ctx, from, "command_result", id, func(c current) bool { return !c.closed() && !c.hasResult },
		func(jctx context.Context, tx *sql.Tx, c current) error {
			if _, err := tx.ExecContext(jctx, `INSERT INTO command_results(command_id, machine_id, epoch, status, reason,
				message, result, received_time) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				id, from.MachineID, from.Epoch, status, reason, msg.text, body, now.UnixMilli()); err != nil {
				return fmt.Errorf("outbox: store result: %w", err)
			}
			return c.update(jctx, tx, now, `UPDATE commands SET state = 'done', op_state = 'DONE', status = ?, reason = ?,
				message = ?, done_time = ?, `+stamped, status, reason, msg.text, now.UnixMilli())
		})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "apply")
	}
	if out == Known && c.late() {
		s.log.InfoContext(ctx, "outbox.late_result", "command_id", id, "machine_id", from.MachineID, "kind", c.kind,
			"status", status, "reason", reason, "late_ms", now.Sub(c.done).Milliseconds())
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

// unlogged is the refusal cause of a result the agent sends without
// logging the command, or "" for any other result.
func unlogged(r typev1.ReasonCode) string {
	switch r {
	case typev1.ReasonCode_REASON_CODE_EPOCH_MISMATCH:
		return "epoch_mismatch"
	case typev1.ReasonCode_REASON_CODE_SEQ_REPLAYED:
		return "seq_replayed"
	default:
		return ""
	}
}
