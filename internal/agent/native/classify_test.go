package native_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/native"
)

// hasExitStatus reads explicit presence through reflection: the getter
// conflates an unset status with 0, and this test must tell them apart.
func hasExitStatus(exited *agentv1.NativeProcessExited) bool {
	fd := exited.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name("exit_status"))
	return exited.ProtoReflect().Has(fd)
}

// scripted returns a host whose fake adapter maps one provider event to want.
func scripted(t *testing.T, want native.Classified) *fixture {
	t.Helper()
	f := newFixture(t)
	f.fa.classify = func(native.RawEvent) native.Classified { return want }
	return f
}

func startForEvents(t *testing.T, f *fixture) {
	t.Helper()
	startSession(t, f, startCmd("command-start"))
	send := sendCmd("command-send", "session-1", 1, "work")
	loggedCommand(t, f.db, send)
	if res := f.h.Run(context.Background(), send); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("send: %+v", res)
	}
}

// Turn end, process exit, cleanup and goal completion stay distinct facts:
// a completed turn emits one turn end and no exit; an exit carries no turn
// claim and never completes anything.
func TestHostTurnEndAndExitAreDistinct(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	startForEvents(t, f)
	ctx := context.Background()
	f.fa.classify = func(raw native.RawEvent) native.Classified {
		switch raw.Kind {
		case "turn_end":
			return native.Classified{Class: native.ClassTurnEnd, Detail: "completed"}
		case "exit":
			return native.Classified{Class: native.ClassExit, Detail: "0"}
		default:
			return native.Classified{Class: native.ClassUnknown, Name: raw.Kind}
		}
	}

	if err := f.h.EmitRaw(ctx, "session-1", native.RawEvent{Kind: "turn_end", Turn: "turn-session-1"}); err != nil {
		t.Fatal(err)
	}
	arms := f.em.arms()
	if len(arms) != 1 || arms[0] != "native_turn_ended" {
		t.Fatalf("arms %v, want one native_turn_ended", arms)
	}
	got := f.em.all()[0].GetNativeTurnEnded()
	if got.GetOutcome() != agentv1.NativeTurnOutcome_NATIVE_TURN_OUTCOME_COMPLETED {
		t.Fatalf("outcome %v, want COMPLETED", got.GetOutcome())
	}
	if got.GetTurn() != 2 {
		t.Fatalf("turn %d, want the send's admission order 2", got.GetTurn())
	}
	if n := countEffects(t, f, ""); n != 2 {
		t.Fatalf("turn end created journal effects: %d total", n)
	}

	if err := f.h.EmitRaw(ctx, "session-1", native.RawEvent{Kind: "exit"}); err != nil {
		t.Fatal(err)
	}
	arms = f.em.arms()
	if len(arms) != 2 || arms[1] != "native_process_exited" {
		t.Fatalf("arms %v, want turn end then exit", arms)
	}
	exited := f.em.all()[1].GetNativeProcessExited()
	if !hasExitStatus(exited) || exited.GetExitStatus() != 0 {
		t.Fatalf("exit status %v, want a present 0", exited.GetExitStatus())
	}
}

// An unreadable exit status stays unset: never read as success.
func TestHostUnreadableExitStatusStaysUnset(t *testing.T) {
	t.Parallel()
	f := scripted(t, native.Classified{Class: native.ClassExit, Name: "SIGKILL"})
	startForEvents(t, f)
	if err := f.h.EmitRaw(context.Background(), "session-1", native.RawEvent{Kind: "exit"}); err != nil {
		t.Fatal(err)
	}
	exited := f.em.all()[len(f.em.all())-1].GetNativeProcessExited()
	if hasExitStatus(exited) {
		t.Fatalf("exit status %v, want unset", exited.GetExitStatus())
	}
	if exited.GetSignal() != "SIGKILL" {
		t.Fatalf("signal %q, want SIGKILL", exited.GetSignal())
	}
}

// Unknown SDK events are recorded as identity only: backend, raw kind and
// payload digest. They never end a turn, never touch the journal and never
// change control state.
func TestHostUnknownEventsRecordedNeverCompleted(t *testing.T) {
	t.Parallel()
	f := newFixture(t) // no classify override: the default maps everything unknown
	startForEvents(t, f)
	ctx := context.Background()
	payload := []byte(`{"future":"event","n":42}`)
	if err := f.h.EmitRaw(ctx, "session-1", native.RawEvent{Kind: "Frobnicate", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	arms := f.em.arms()
	if len(arms) != 1 || arms[0] != "native_unknown" {
		t.Fatalf("arms %v, want one native_unknown", arms)
	}
	unk := f.em.all()[0].GetNativeUnknown()
	if unk.GetRawKind() != "frobnicate" {
		t.Fatalf("raw kind %q, want lowercased provider kind", unk.GetRawKind())
	}
	sum := sha256.Sum256(payload)
	if unk.GetDigest() != hex.EncodeToString(sum[:]) {
		t.Fatal("unknown event digest is not the payload digest")
	}
	if unk.GetBackend() != typev1.NativeBackend_NATIVE_BACKEND_CLAUDE_CODE {
		t.Fatalf("backend %v, want the session's", unk.GetBackend())
	}
	if n := countEffects(t, f, "confirmed"); n != 2 {
		t.Fatalf("confirmed effects %d, want start + send only", n)
	}
	s, err := f.db.ReadNativeSession(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if s.ControlState != "managed" {
		t.Fatalf("control state %s, want managed: unknown events never move it", s.ControlState)
	}
}

// Secrets in harness text are scrubbed before persistence and telemetry;
// the home folder becomes ~. The journal and the wire carry no secret.
func TestHostScrubsSecretsBeforeEmitting(t *testing.T) {
	t.Parallel()
	token := "ghp_" + strings.Repeat("A1b2C3d4E5f6", 3)
	f := scripted(t, native.Classified{
		Class: native.ClassOutput,
		Text:  "leaked " + token + " at /home/op/work key=val",
	})
	startForEvents(t, f)
	if err := f.h.EmitRaw(context.Background(), "session-1", native.RawEvent{Kind: "out", Stream: "stdout"}); err != nil {
		t.Fatal(err)
	}
	if len(f.em.all()) != 1 {
		t.Fatalf("events %d, want one output", len(f.em.all()))
	}
	text := f.em.all()[0].GetNativeOutput().GetText()
	if strings.Contains(text, token) {
		t.Fatalf("event carries the secret: %q", text)
	}
	if !strings.Contains(text, "ghp_[redacted]") {
		t.Fatalf("secret not redacted: %q", text)
	}
	if strings.Contains(text, "/home/op/") {
		t.Fatalf("home path not shortened: %q", text)
	}
	if !strings.Contains(text, "~/work") {
		t.Fatalf("home not written as ~: %q", text)
	}
}

// Large output is bounded: chunks are at most 4 KiB, one sample emits at
// most 64 chunks, and the shed bytes ride the first chunk as dropped_bytes.
func TestHostLargeOutputIsBounded(t *testing.T) {
	t.Parallel()
	f := scripted(t, native.Classified{Class: native.ClassOutput, Text: strings.Repeat("x", 300<<10)})
	startForEvents(t, f)
	if err := f.h.EmitRaw(context.Background(), "session-1", native.RawEvent{Kind: "out"}); err != nil {
		t.Fatal(err)
	}
	var total int
	var firstDropped uint64
	for i, ev := range f.em.all() {
		out := ev.GetNativeOutput()
		if out == nil {
			t.Fatalf("event %d is not output", i)
		}
		if len(out.GetText()) > 4<<10 {
			t.Fatalf("chunk %d is %d bytes, over the 4 KiB cap", i, len(out.GetText()))
		}
		total += len(out.GetText())
		if i == 0 {
			firstDropped = out.GetDroppedBytes()
		} else if out.GetDroppedBytes() != 0 {
			t.Fatalf("chunk %d carries dropped_bytes: only the first may", i)
		}
	}
	if n := len(f.em.all()); n != 64 {
		t.Fatalf("chunks %d, want exactly 64", n)
	}
	if total != 64*4<<10 {
		t.Fatalf("emitted %d bytes, want 256 KiB", total)
	}
	if firstDropped == 0 {
		t.Fatal("no dropped_bytes on the first chunk for a 300 KiB sample")
	}
	if want := uint64(300<<10 - total); firstDropped != want {
		t.Fatalf("dropped_bytes %d, want %d", firstDropped, want)
	}
}

// Approval requests and usage publish directly: a full output pipeline
// never delays them, and usage coalesces per session.
func TestHostApprovalAndUsagePublishDirectly(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	startForEvents(t, f)
	ctx := context.Background()
	f.fa.classify = func(raw native.RawEvent) native.Classified {
		switch raw.Kind {
		case "approval":
			return native.Classified{Class: native.ClassApproval, Name: "appr-1", Detail: "rm", Text: "delete the build dir"}
		default:
			return native.Classified{Class: native.ClassUsage, InputTokens: 100, OutputTokens: 50}
		}
	}
	if err := f.h.EmitRaw(ctx, "session-1", native.RawEvent{Kind: "approval", Turn: "turn-session-1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.h.EmitRaw(ctx, "session-1", native.RawEvent{Kind: "usage", Turn: "turn-session-1"}); err != nil {
		t.Fatal(err)
	}
	arms := f.em.arms()
	if len(arms) != 2 || arms[0] != "native_approval" || arms[1] != "native_usage" {
		t.Fatalf("arms %v, want approval then usage", arms)
	}
	appr := f.em.all()[0].GetNativeApproval()
	if appr.GetApprovalId() != "appr-1" || appr.GetTool() != "rm" || appr.GetTurn() != 2 {
		t.Fatalf("approval %+v, want id/tool/turn 2", appr)
	}
	use := f.em.all()[1].GetNativeUsage()
	if use.GetInputTokens() != 100 || use.GetOutputTokens() != 50 || use.GetAccount().GetValue() != "account-1" {
		t.Fatalf("usage %+v", use)
	}
}

// Events for an unknown session are refused before classification: there
// is no session to attribute them to.
func TestHostEmitForUnknownSessionFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := f.h.EmitRaw(context.Background(), "ghost", native.RawEvent{Kind: "out"}); err == nil {
		t.Fatal("emitted for an unknown session")
	}
	if len(f.em.all()) != 0 {
		t.Fatal("event published for an unknown session")
	}
}
