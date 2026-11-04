package attach

import (
	"context"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// openRequest wraps an OpenAttach in a command for the handler.
func openRequest(cmd *agentv1.OpenAttach) exec.Request {
	return exec.Request{Command: &agentv1.Command{Kind: &agentv1.Command_OpenAttach{OpenAttach: cmd}}}
}

// TestOpenAttachHandlerRoundTrip: a good command opens and answers the
// view; running it again (or resuming) answers the same view.
func TestOpenAttachHandlerRoundTrip(t *testing.T) {
	t.Parallel()
	program, _ := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	h := m.OpenHandler()
	if h.Kind() != "OpenAttach" {
		t.Fatalf("Kind = %q, want OpenAttach", h.Kind())
	}
	if h.Class(nil) != exec.RepeatSafe {
		t.Fatalf("Class = %v, want repeat_safe", h.Class(nil))
	}
	if caps := h.Capabilities(); len(caps) != 1 || caps[0].GetName() != "attach.pty" {
		t.Fatalf("Capabilities = %v, want [attach.pty]", caps)
	}
	ctx := context.WithValue(context.Background(), streamKey{}, "h1")
	res := h.Run(ctx, openRequest(openCmd("h1", "work", 80, 24, false)))
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("Run = %v %v, want OK", res.GetStatus(), res.GetReason())
	}
	openRes, ok := res.GetDetail().(*agentv1.CommandResult_OpenAttach)
	if !ok {
		t.Fatalf("detail = %T, want OpenAttach", res.GetDetail())
	}
	got := openRes.OpenAttach.GetViewSessionName()
	if want := "fleet-view-h1"; got != want {
		t.Fatalf("view = %q, want %q", got, want)
	}
	again := h.Run(ctx, openRequest(openCmd("h1", "work", 80, 24, false)))
	againRes, ok := again.GetDetail().(*agentv1.CommandResult_OpenAttach)
	if !ok || againRes.OpenAttach.GetViewSessionName() != got {
		t.Fatal("re-run opened a second view, want the live one")
	}
	resumed := h.Resume(ctx, openRequest(openCmd("h1", "work", 80, 24, false)))
	if resumed.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("Resume = %v, want OK", resumed.GetStatus())
	}
}

// TestOpenAttachHandlerRefusals: the wrong kind fails INTERNAL, a bad
// open INVALID_ARGUMENT, and a missing target SESSION_NOT_FOUND.
func TestOpenAttachHandlerRefusals(t *testing.T) {
	t.Parallel()
	program, _ := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	h := m.OpenHandler()
	ctx := context.Background()
	if res := h.Run(ctx, exec.Request{Command: &agentv1.Command{}}); res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("wrong kind = %v, want INTERNAL", res.GetReason())
	}
	res := h.Run(ctx, openRequest(openCmd("", "work", 80, 24, false)))
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT || res.GetRetryable() {
		t.Fatalf("bad id = %v %v retryable=%v, want FAILED/INVALID_ARGUMENT", res.GetStatus(), res.GetReason(), res.GetRetryable())
	}
	res = h.Run(context.WithValue(ctx, streamKey{}, "gone"), openRequest(openCmd("h9", "gone", 80, 24, false)))
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND {
		t.Fatalf("missing target = %v %v, want FAILED/SESSION_NOT_FOUND", res.GetStatus(), res.GetReason())
	}
}

// TestCloseAttachHandlerRoundTrip: closing ends the attach and answers OK;
// closing again or an unknown id still answers OK.
func TestCloseAttachHandlerRoundTrip(t *testing.T) {
	t.Parallel()
	program, _ := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	oh, ch := m.OpenHandler(), m.CloseHandler()
	if ch.Kind() != "CloseAttach" {
		t.Fatalf("Kind = %q, want CloseAttach", ch.Kind())
	}
	if ch.Class(nil) != exec.RepeatSafe {
		t.Fatalf("Class = %v, want repeat_safe", ch.Class(nil))
	}
	ctx := context.WithValue(context.Background(), streamKey{}, "c1")
	if res := oh.Run(ctx, openRequest(openCmd("c1", "work", 80, 24, false))); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("open = %v, want OK", res.GetStatus())
	}
	closeReq := func(id string) exec.Request {
		return exec.Request{Command: &agentv1.Command{Kind: &agentv1.Command_CloseAttach{
			CloseAttach: &agentv1.CloseAttach{AttachId: &typev1.AttachId{Value: id}},
		}}}
	}
	if res := ch.Run(ctx, closeReq("c1")); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("close = %v, want OK", res.GetStatus())
	}
	if res := ch.Run(ctx, closeReq("c1")); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("re-close = %v, want OK", res.GetStatus())
	}
	if res := ch.Run(ctx, closeReq("never-opened")); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("unknown close = %v, want OK", res.GetStatus())
	}
	if res := ch.Run(ctx, closeReq("")); res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("empty close = %v, want INVALID_ARGUMENT", res.GetReason())
	}
	if res := ch.Run(ctx, exec.Request{Command: &agentv1.Command{}}); res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("wrong kind = %v, want INTERNAL", res.GetReason())
	}
	if ids := m.OpenIDs(); len(ids) != 0 {
		t.Fatalf("OpenIDs = %q, want drained", ids)
	}
}
