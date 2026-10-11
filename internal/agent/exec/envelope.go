package exec

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// maxCommandID caps a command_id the executor logs (ids.proto: 1 to 128).
const maxCommandID = 128

// plan is what the checks after the log decided for one command: a
// refusal to record, or a handler to run.
type plan struct {
	refusal  *agentv1.CommandResult
	handler  Handler
	cell     cell
	approval bool
	resume   bool // a running entry after a restart: Resume or run again
}

// envelope runs the checks of 02-contracts "Envelope checks" in order on
// one command: known id, deadline, epoch, seq; then logs it with its seq
// advance in one store job, acks it, and hands the rest (requires, policy,
// role, approval, the run) to the command's goroutine.
func (e *Executor) envelope(ctx context.Context, c *agentv1.Command) {
	id, kind := c.GetCommandId().GetValue(), kindOf(c)
	if !printableID(id) {
		e.log.WarnContext(ctx, "command.dropped", "reason", "invalid_command_id", "kind", kind)
		return
	}
	e.log.InfoContext(ctx, "command.received", "command_id", id, "kind", kind, "epoch", c.GetEpoch(), "seq", c.GetSeq())
	if ent, err := e.opts.DB.Command(ctx, id); err == nil {
		e.answerKnown(ctx, ent)
		return
	} else if !errors.Is(err, ring.ErrUnknownCommand) {
		e.storeFailed(ctx, id, "read", err)
		return
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(c)
	if err != nil {
		e.storeFailed(ctx, id, "encode", err)
		return
	}
	now := e.opts.Now()
	rc := ring.Command{
		ID: id, Kind: kind, Epoch: c.GetEpoch(), Seq: c.GetSeq(),
		Received: now, NotAfter: c.GetNotAfterTime().AsTime(), Body: body,
	}
	if e.past(c) {
		// Logged, acked and answered, but its seq is not applied: the
		// epoch check comes after the deadline (ruling fork 4 i).
		ent, dup, err := e.opts.DB.LogCommand(ctx, rc)
		if err != nil || dup {
			e.loggedOr(ctx, id, ent, dup, err)
			return
		}
		e.accepted(ctx, kind, ent, now)
		e.start(ctx, c, ent, plan{refusal: refusal(typev1.Status_STATUS_EXPIRED, typev1.ReasonCode_REASON_CODE_EXPIRED,
			"the command reached the agent after its deadline")})
		return
	}
	ent, dup, err := e.opts.DB.LogAndApply(ctx, rc)
	switch {
	case errors.Is(err, ring.ErrWrongEpoch):
		r := refusal(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_EPOCH_MISMATCH,
			"the command belongs to another connection epoch")
		r.Retryable = true
		e.unlogged(ctx, c, kind, r)
		return
	case errors.Is(err, ring.ErrSeqReplayed):
		e.unlogged(ctx, c, kind, refusal(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_SEQ_REPLAYED,
			"the command's seq was already applied in this epoch"))
		return
	case err != nil || dup:
		e.loggedOr(ctx, id, ent, dup, err)
		return
	}
	e.accepted(ctx, kind, ent, now)
	e.start(ctx, c, ent, e.decide(c, kind, ent, false))
}

// past reports whether c's deadline has passed in strict hub time; a hub
// time the agent cannot read counts as past (02i "Clocks", ruling fork 11).
func (e *Executor) past(c *agentv1.Command) bool {
	now, ok := e.opts.Clock.Now()
	return !ok || !now.Before(c.GetNotAfterTime().AsTime())
}

// accepted counts a logged command in the rate window and acks it: the ack
// follows the commit (CommandAck means received and persisted).
func (e *Executor) accepted(ctx context.Context, kind string, ent ring.Entry, now time.Time) {
	e.window.add(kind, now)
	e.ack(ctx, ent)
}

func (e *Executor) ack(ctx context.Context, ent ring.Entry) {
	e.send(ctx, ent.Kind, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandAck{CommandAck: &agentv1.CommandAck{
		CommandId: &typev1.CommandId{Value: ent.ID}, ReceivedTime: timestamppb.New(ent.Received),
	}}})
}

// loggedOr answers a command the log already holds, or reports the store
// error; the hub's re-send retries a command whose log write failed.
func (e *Executor) loggedOr(ctx context.Context, id string, ent ring.Entry, dup bool, err error) {
	if err != nil {
		e.storeFailed(ctx, id, "log", err)
		return
	}
	if dup {
		e.answerKnown(ctx, ent)
	}
}

// answerKnown answers a known command_id from its entry without checking
// or running it again: the logged result, or for an entry with none yet
// another ack; the run already started sends the one result (ruling fork 4
// ii; 02i).
func (e *Executor) answerKnown(ctx context.Context, ent ring.Entry) {
	if ent.Status != ring.StatusFinished {
		e.ack(ctx, ent)
		return
	}
	var r agentv1.CommandResult
	if err := proto.Unmarshal(ent.Result, &r); err != nil {
		e.storeFailed(ctx, ent.ID, "decode", err)
		return
	}
	e.send(ctx, ent.Kind, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandResult{CommandResult: &r}})
}

// decide runs the checks after the log: requires, policy (safe mode, rate
// limit), role, then whether a local approval is needed. A resumed entry
// skips the rate window (it was counted when logged) and the approval it
// already had.
func (e *Executor) decide(c *agentv1.Command, kind string, ent ring.Entry, resumed bool) plan {
	h := e.reg[kind]
	if !e.reg.supports(kind, c.GetRequires()) {
		r := refusal(typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
			"this agent does not advertise the capability the command needs")
		if req := c.GetRequires(); req != nil {
			r.MissingCapability = &typev1.Capability{Name: req.GetName(), Version: req.GetVersion()}
		}
		return plan{refusal: r}
	}
	pol := e.opts.Policy
	if pol.SafeMode() && !observeOnly(c, kind, h) {
		return plan{refusal: refusal(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_POLICY_SAFE_MODE,
			"the agent is in policy safe mode and runs observe commands only")}
	}
	if !resumed {
		limit := DefaultRateLimit
		if n, ok := pol.RateLimit(kind); ok && n < limit {
			limit = n
		}
		if wait, over := e.window.over(kind, e.opts.Now(), limit); over {
			r := refusal(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_RATE_LIMITED,
				"too many commands of this kind in the last minute")
			r.Retryable, r.RetryAfter = true, durationpb.New(wait)
			return plan{refusal: r}
		}
	}
	cl, ok := roleCell(c, kind, h)
	if !ok {
		return plan{refusal: refusal(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT,
			"the requester's role may not ask for this command")}
	}
	p := plan{handler: h, cell: cl}
	if ent.Status == ring.StatusRunning {
		p.resume = true
		return p
	}
	// A local rule can make any allowed cell wait, never loosen one (02g:
	// the local policy is the ceiling).
	p.approval = cl.alwaysLocal || pol.ApprovalRequired(kind)
	return p
}

// start hands c to its own goroutine, once per command_id.
func (e *Executor) start(ctx context.Context, c *agentv1.Command, ent ring.Entry, p plan) {
	if !e.claim(ent.ID, ent.Kind) {
		return
	}
	e.launch(ctx, c, ent, p)
}

// launch starts the goroutine of a command already claimed.
func (e *Executor) launch(ctx context.Context, c *agentv1.Command, ent ring.Entry, p plan) {
	e.wg.Add(1)
	go e.do(ctx, c, ent, p)
}

func refusal(st typev1.Status, why typev1.ReasonCode, msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: st, Reason: why, Message: msg}
}

func printableID(s string) bool {
	if s == "" || len(s) > maxCommandID {
		return false
	}
	for i := range len(s) {
		if s[i] <= ' ' || s[i] >= 0x7f {
			return false
		}
	}
	return true
}
