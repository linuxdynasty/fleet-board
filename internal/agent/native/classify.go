package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// parseExitStatus reads a small decimal status; anything else is unreadable
// and stays unset on the wire.
func parseExitStatus(s string) (int32, error) {
	u, err := strconv.ParseUint(strings.TrimSpace(s), 10, 8)
	if err != nil {
		return 0, err
	}
	return int32(u), nil
}

// Classification caps (02e "Size limits"): every free-text field is capped
// at its wire cap and control-stripped by the scrubber before it is stored
// or emitted. Secrets are scrubbed before persistence and telemetry.
const (
	outputCap  = 4 << 10
	noteCap    = 1 << 10
	nameCap    = 128
	signalCap  = 32
	rawKindCap = 128
)

// scrubOpt is the host's scrub setting: field caps plus the agent's home
// written as ~, matching the executor's message rule.
func (h *Host) scrubOpt(max int) scrub.Options {
	return scrub.Options{MaxBytes: max, Home: h.home}
}

// defaultClassify maps a provider event with no registered adapter to
// unknown: without qualification nothing is interpreted, and nothing
// completes. Adapters override Classify with their backend's event map.
func defaultClassify(raw RawEvent) Classified {
	return Classified{Class: ClassUnknown, Name: raw.Kind}
}

// EmitRaw classifies one provider event for a managed session, scrubs and
// caps its text, and publishes the wire event. Output and tool samples go
// through the bounded buffer (shed with gap counts); approval, usage, turn
// end, exit and unknown events publish directly so a full output buffer
// never delays them. Unknown events record identity only and never touch
// the journal, control state or completion. A nil emitter skips publication
// but still buffers and counts (tests observe the counts).
func (h *Host) EmitRaw(ctx context.Context, sessionID string, raw RawEvent) error {
	session, err := h.db.ReadNativeSession(ctx, sessionID)
	if err != nil {
		return err
	}
	backend, _ := BackendOf(session.Harness)
	var c Classified
	if a, ok := h.adapterFor(backend); ok {
		c = a.Classify(raw)
	} else {
		c = defaultClassify(raw)
	}
	turn := h.turnOrder(sessionID, raw.Turn)
	switch c.Class {
	case ClassOutput:
		return h.emitOutput(ctx, sessionID, turn, raw.Stream, c.Text)
	case ClassTool:
		return h.emitTool(ctx, sessionID, turn, c)
	case ClassApproval:
		return h.emitOne(ctx, &agentv1.Event{Body: &agentv1.Event_NativeApproval{
			NativeApproval: &agentv1.NativeApprovalRequest{
				Session: &typev1.SessionId{Value: sessionID}, Turn: turn,
				ApprovalId: cut(scrub.Text(c.Name, h.scrubOpt(nameCap)), nameCap),
				Tool:       cut(scrub.Text(c.Detail, h.scrubOpt(nameCap)), nameCap),
				Prompt:     scrub.Text(c.Text, h.scrubOpt(noteCap)),
			},
		}}, "native_approval")
	case ClassUsage:
		return h.emitOne(ctx, &agentv1.Event{Body: &agentv1.Event_NativeUsage{
			NativeUsage: &agentv1.NativeUsage{
				Session: &typev1.SessionId{Value: sessionID}, Turn: turn,
				Account:     &typev1.AccountId{Value: session.AccountRef},
				InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
			},
		}}, "native_usage")
	case ClassTurnEnd:
		return h.emitOne(ctx, &agentv1.Event{Body: &agentv1.Event_NativeTurnEnded{
			NativeTurnEnded: &agentv1.NativeTurnEnded{
				Session: &typev1.SessionId{Value: sessionID}, Turn: turn,
				Outcome: turnOutcome(c.Detail),
				Error:   scrub.Text(c.Text, h.scrubOpt(noteCap)),
			},
		}}, "native_turn_ended")
	case ClassExit:
		exited := &agentv1.NativeProcessExited{
			Session:  &typev1.SessionId{Value: sessionID},
			Signal:   cut(scrub.Text(c.Name, h.scrubOpt(signalCap)), signalCap),
			Expected: c.Expected,
		}
		if n, err := parseExitStatus(c.Detail); err == nil {
			exited.ExitStatus = &n
		}
		// An unreadable status stays unset: never read as success.
		return h.emitOne(ctx, &agentv1.Event{Body: &agentv1.Event_NativeProcessExited{
			NativeProcessExited: exited,
		}}, "native_process_exited")
	default:
		kind := cut(strings.ToLower(strings.TrimSpace(raw.Kind)), rawKindCap)
		if kind == "" {
			kind = "unreadable"
		}
		sum := sha256.Sum256(raw.Payload)
		return h.emitOne(ctx, &agentv1.Event{Body: &agentv1.Event_NativeUnknown{
			NativeUnknown: &agentv1.NativeUnknownEvent{
				Session: &typev1.SessionId{Value: sessionID}, Backend: backend,
				RawKind: scrub.Text(kind, h.scrubOpt(rawKindCap)),
				Digest:  hex.EncodeToString(sum[:]),
			},
		}}, "native_unknown")
	}
}

// turnOutcome maps the adapter's turn detail to the wire outcome. Anything
// else is UNCERTAIN: the host never upgrades an unclear ending into a
// completion.
func turnOutcome(detail string) agentv1.NativeTurnOutcome {
	switch detail {
	case "completed":
		return agentv1.NativeTurnOutcome_NATIVE_TURN_OUTCOME_COMPLETED
	case "interrupted":
		return agentv1.NativeTurnOutcome_NATIVE_TURN_OUTCOME_INTERRUPTED
	case "failed":
		return agentv1.NativeTurnOutcome_NATIVE_TURN_OUTCOME_FAILED
	default:
		return agentv1.NativeTurnOutcome_NATIVE_TURN_OUTCOME_UNCERTAIN
	}
}

// Output bounds: one harness-output chunk is at most 4 KiB (the
// NativeOutput cap), and one sample emits at most 64 chunks (256 KiB).
// Larger harness bursts are cut with the gap counted, so a slow consumer
// drops data instead of blocking approvals, interrupt or heartbeat.
const (
	outputChunkCap = 4 << 10
	outputBatchCap = outputChunkCap * 64
)

// emitOutput publishes one output sample as ≤4 KiB chunks. Shed bytes ride
// the first chunk as dropped_bytes; the totals ride the buffer-dropped
// metric. Chunk splits respect rune boundaries.
func (h *Host) emitOutput(ctx context.Context, sessionID string, turn uint64, stream, text string) error {
	shed := 0
	if len(text) > outputBatchCap {
		shed = len(text) - outputBatchCap
		text = text[:outputBatchCap]
	}
	clean := scrub.Text(text, h.scrubOpt(outputBatchCap))
	pieces := splitRunes(clean, outputChunkCap)
	if len(pieces) > 64 {
		for _, p := range pieces[64:] {
			shed += len(p)
		}
		pieces = pieces[:64]
	}
	if shed > 0 {
		h.metrics.shed(int64(shed), "output")
	}
	var st agentv1.NativeStream
	if stream == "stderr" {
		st = agentv1.NativeStream_NATIVE_STREAM_STDERR
	} else {
		st = agentv1.NativeStream_NATIVE_STREAM_STDOUT
	}
	for i, p := range pieces {
		ev := &agentv1.Event{Body: &agentv1.Event_NativeOutput{
			NativeOutput: &agentv1.NativeOutput{
				Session: &typev1.SessionId{Value: sessionID}, Turn: turn,
				Stream: st, Text: p,
			},
		}}
		if i == 0 {
			ev.GetNativeOutput().DroppedBytes = uint64(shed) //nolint:gosec // G115: shed grows only by non-negative byte counts.
		}
		if err := h.emitOne(ctx, ev, "native_output"); err != nil {
			return err
		}
	}
	return nil
}

// splitRunes cuts s into pieces of at most n bytes without splitting a
// rune.
func splitRunes(s string, n int) []string {
	var out []string
	for len(s) > 0 {
		if len(s) <= n {
			out = append(out, s)
			break
		}
		i := n
		for i > 0 && s[i]>>6 == 0x2 {
			i--
		}
		if i == 0 {
			i = n
		}
		out = append(out, s[:i])
		s = s[i:]
	}
	return out
}

// emitTool publishes one tool-activity sample. The sample is capped at
// 1 KiB; the turn end and usage below stay the durable anchors.
func (h *Host) emitTool(ctx context.Context, sessionID string, turn uint64, c Classified) error {
	phase := agentv1.NativeToolPhase_NATIVE_TOOL_PHASE_UNSPECIFIED
	switch c.Detail {
	case "started":
		phase = agentv1.NativeToolPhase_NATIVE_TOOL_PHASE_STARTED
	case "finished":
		phase = agentv1.NativeToolPhase_NATIVE_TOOL_PHASE_FINISHED
	case "failed":
		phase = agentv1.NativeToolPhase_NATIVE_TOOL_PHASE_FAILED
	}
	return h.emitOne(ctx, &agentv1.Event{Body: &agentv1.Event_NativeTool{
		NativeTool: &agentv1.NativeToolActivity{
			Session: &typev1.SessionId{Value: sessionID}, Turn: turn,
			Tool:   cut(scrub.Text(c.Name, h.scrubOpt(nameCap)), nameCap),
			Phase:  phase,
			Detail: scrub.Text(c.Text, h.scrubOpt(noteCap)),
		},
	}}, "native_tool")
}

// emitOne stamps the time and hands the event to the emitter (nil skips
// publication). Every event is counted by arm, including the refused path
// the emitter itself reports.
func (h *Host) emitOne(ctx context.Context, ev *agentv1.Event, arm string) error {
	ev.ObservedTime = timestamppb.New(h.now())
	if h.emit == nil {
		return nil
	}
	if _, err := h.emit.Emit(ctx, ev); err != nil {
		return err
	}
	h.metrics.emitted(arm)
	h.log.InfoContext(ctx, "native.event_emitted", "event", arm, "session", sessionOf(ev))
	return nil
}

// turnOrder maps a provider turn identity to the journal admission order of
// the send that opened it. Unknown or empty identities are turn 0:
// unnumbered, never another turn's number.
func (h *Host) turnOrder(sessionID, providerTurn string) uint64 {
	if providerTurn == "" {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turns == nil {
		return 0
	}
	return h.turns[sessionID][providerTurn]
}

// noteTurn records the provider identity of an admitted turn for later
// event correlation. Ephemeral: a restart forgets it; the journal order is
// the durable turn number on the wire.
func (h *Host) noteTurn(sessionID, providerTurn string, order uint64) {
	if providerTurn == "" || order == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turns == nil {
		h.turns = map[string]map[string]uint64{}
	}
	m, ok := h.turns[sessionID]
	if !ok {
		m = map[string]uint64{}
		h.turns[sessionID] = m
	}
	m[providerTurn] = order
}

// sessionOf reads the session id back out of a built event for the log
// line. Every native arm carries session first.
func sessionOf(ev *agentv1.Event) string {
	switch b := ev.GetBody().(type) {
	case *agentv1.Event_NativeOutput:
		return b.NativeOutput.GetSession().GetValue()
	case *agentv1.Event_NativeTool:
		return b.NativeTool.GetSession().GetValue()
	case *agentv1.Event_NativeApproval:
		return b.NativeApproval.GetSession().GetValue()
	case *agentv1.Event_NativeUsage:
		return b.NativeUsage.GetSession().GetValue()
	case *agentv1.Event_NativeTurnEnded:
		return b.NativeTurnEnded.GetSession().GetValue()
	case *agentv1.Event_NativeProcessExited:
		return b.NativeProcessExited.GetSession().GetValue()
	case *agentv1.Event_NativeUnknown:
		return b.NativeUnknown.GetSession().GetValue()
	default:
		return ""
	}
}

// cut shortens s to n bytes; the scrub already cut it, so this only guards
// the wire cap when the two disagree.
func cut(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
