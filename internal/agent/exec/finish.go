package exec

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// Free text caps (02e "Size limits"): a result or progress message is at
// most 1 KiB after the scrub. preCut bounds the scrubber's input, and the
// cut never splits a line (scrub.Bound; internal/scrub README "Callers
// bound the input"): a secret is never cut short and left unmatched.
const (
	maxMessage = 1 << 10
	preCut     = 8 << 10
)

// finish records r as the command's one result, then queues it: committed
// before queued (ruling fork 12 b). A result already logged wins and is the
// one sent.
func (e *Executor) finish(ctx context.Context, c *agentv1.Command, ent ring.Entry, r *agentv1.CommandResult, started time.Time, sp *span) {
	done := e.opts.Now()
	r.CommandId = &typev1.CommandId{Value: ent.ID}
	r.Message = e.text(r.GetMessage())
	if !started.IsZero() {
		r.StartedTime = timestamppb.New(started)
	}
	r.FinishedTime = timestamppb.New(done)
	r.Trace = sp.trace()
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(r)
	if err != nil {
		e.storeFailed(ctx, ent.ID, "encode", err)
		return
	}
	switch err := e.opts.DB.RecordResult(ctx, ent.ID, b, done); {
	case errors.Is(err, ring.ErrResultLogged):
		logged, rerr := e.opts.DB.Command(ctx, ent.ID)
		if rerr != nil {
			e.storeFailed(ctx, ent.ID, "read", rerr)
			return
		}
		e.answerKnown(ctx, logged)
		return
	case err != nil:
		e.storeFailed(ctx, ent.ID, "result", err)
		return
	}
	var elapsed time.Duration
	if !started.IsZero() {
		elapsed = done.Sub(started)
	}
	e.outcome(ctx, c, ent.Kind, r, elapsed, started.IsZero())
	sp.status(r)
	e.send(ctx, ent.Kind, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandResult{CommandResult: r}})
}

// unlogged answers EPOCH_MISMATCH or SEQ_REPLAYED with one result that is
// neither logged nor acked: the hub re-issues the same command_id under the
// new epoch, which a logged refusal would answer forever (ruling fork 4 i).
func (e *Executor) unlogged(ctx context.Context, c *agentv1.Command, kind string, r *agentv1.CommandResult) {
	r.CommandId = &typev1.CommandId{Value: c.GetCommandId().GetValue()}
	r.Message = e.text(r.GetMessage())
	r.FinishedTime = timestamppb.New(e.opts.Now())
	e.outcome(ctx, c, kind, r, 0, true)
	e.send(ctx, kind, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandResult{CommandResult: r}})
}

// outcome logs, counts and audits one result. refused is a result the
// executor's own checks produced (no handler ran).
func (e *Executor) outcome(ctx context.Context, c *agentv1.Command, kind string, r *agentv1.CommandResult, elapsed time.Duration, refused bool) {
	id := r.GetCommandId().GetValue()
	st, why := enumName(r.GetStatus().String(), "STATUS_"), enumName(r.GetReason().String(), "REASON_CODE_")
	e.m.result(ctx, kind, st, why, elapsed, refused)
	if refused {
		e.refused.Add(1)
		e.log.InfoContext(ctx, "command.refused", "command_id", id, "kind", kind, "status", st, "reason", why)
	} else {
		e.log.InfoContext(ctx, "command.finished", "command_id", id, "kind", kind, "status", st, "reason", why,
			"elapsed_ms", elapsed.Milliseconds())
	}
	// 02g "Audit of decisions made at the machine": every
	// REJECTED_BY_POLICY result, whatever its reason (LOCAL_DENIED and
	// LOCAL_CONFIRM_TIMEOUT are such results), is a command.refused row.
	if r.GetStatus() == typev1.Status_STATUS_REJECTED_BY_POLICY {
		e.audit(audit.Entry{
			Action: audit.CommandRefused, CommandID: id, Kind: kind, Status: st, Reason: why,
			RequestedBy: principal(c.GetRequestedBy()),
		})
	}
}

func (e *Executor) audit(a audit.Entry) {
	if e.opts.Audit != nil {
		e.opts.Audit.Append(a)
	}
}

// storeFailed logs and counts an agent.db failure. The command stays as
// the log left it: an unlogged one is re-sent by the hub at its next
// connect; a logged one with no result (a failed SetRunning or
// RecordResult) is taken up by the resume pass at the next connect.
func (e *Executor) storeFailed(ctx context.Context, id, step string, err error) {
	args := []any{"step", step, "err_kind", errKind(err)}
	if printableID(id) {
		args = append(args, "command_id", id)
	}
	e.m.storeFailed(ctx, step)
	e.log.WarnContext(ctx, "command.store_failed", args...)
}

func errKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "canceled"
	default:
		return "store"
	}
}

// text is agent-written free text as it may leave the machine: cut back
// to whole lines within preCut (or "" when its first 8 KiB hold no line
// end), scrubbed with the home folder written as ~, at most 1 KiB (02e;
// internal/scrub README "Callers bound the input").
func (e *Executor) text(s string) string {
	if s == "" {
		return ""
	}
	s, _ = scrub.Bound(s, preCut)
	return scrub.Text(s, scrub.Options{MaxBytes: maxMessage, Home: e.opts.Home})
}

func enumName(s, prefix string) string { return strings.TrimPrefix(s, prefix) }

// closedValue is the audit log's one shape for a free field.
var closedValue = regexp.MustCompile(`^[A-Za-z0-9_.:-]{0,128}$`)

func closedOr(s string) string {
	if closedValue.MatchString(s) {
		return s
	}
	return ""
}

// principal maps a requester to the audit row's closed fields: the kind
// without its prefix, and the id with "/" written as ":" (device/<id>
// becomes device:<id>); an id that is still not a closed value is left
// out. The hub's permanent copy keeps the id as sent.
func principal(p *typev1.Principal) *audit.Principal {
	if p == nil {
		return nil
	}
	return &audit.Principal{
		Kind: strings.ToLower(enumName(p.GetKind().String(), "PRINCIPAL_KIND_")),
		ID:   closedOr(strings.ReplaceAll(p.GetId().GetValue(), "/", ":")),
	}
}
