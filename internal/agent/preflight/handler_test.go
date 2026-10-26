package preflight

import (
	"context"
	"errors"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

func preflightCmd() *agentv1.Command {
	return &agentv1.Command{
		Kind: &agentv1.Command_Preflight{Preflight: &agentv1.Preflight{Spec: testSpec()}},
	}
}

func TestHandlerKindAndCapability(t *testing.T) {
	t.Parallel()
	h := Handler{Checker: &Checker{Reader: stubReader{facts: goodFacts()}}}
	if h.Kind() != "Preflight" {
		t.Errorf("Kind = %q, want Preflight", h.Kind())
	}
	caps := h.Capabilities()
	if len(caps) != 1 || caps[0].GetName() != "preflight" || caps[0].GetVersion() != 1 {
		t.Errorf("Capabilities = %v, want [{preflight 1}]", caps)
	}
	if h.Class(preflightCmd()) != exec.RepeatSafe {
		t.Errorf("Class = %v, want repeat_safe: preflight only reads", h.Class(preflightCmd()))
	}
}

func TestHandlerRun(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		facts      Facts
		wantStatus typev1.Status
		wantOK     bool
		wantWhy    []typev1.ReasonCode
	}{
		{name: "clear", facts: goodFacts(), wantStatus: typev1.Status_STATUS_OK, wantOK: true},
		{
			name: "refusal is still status OK with reasons",
			facts: Facts{
				SignedInKnown:   true,
				FolderUntrusted: true,
				DiskKnown:       true,
				DiskFreeBytes:   20 * 1024 * 1024 * 1024,
				PushKnown:       true,
			},
			wantStatus: typev1.Status_STATUS_OK,
			wantOK:     false,
			wantWhy: []typev1.ReasonCode{
				typev1.ReasonCode_REASON_CODE_HARNESS_NOT_SIGNED_IN,
				typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING,
				typev1.ReasonCode_REASON_CODE_PUSH_ACCESS_MISSING,
				typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			typer := &countTyper{}
			h := Handler{Checker: &Checker{Reader: stubReader{facts: tc.facts}, Typer: typer}}
			res := h.Run(context.Background(), exec.Request{Command: preflightCmd()})
			if res.GetStatus() != tc.wantStatus {
				t.Errorf("status = %v, want %v", res.GetStatus(), tc.wantStatus)
			}
			got := res.GetPreflight()
			if got == nil {
				t.Fatal("preflight detail is unset")
			}
			if got.GetOk() != tc.wantOK {
				t.Errorf("ok = %v, want %v", got.GetOk(), tc.wantOK)
			}
			sameReasons(t, reasons(got), tc.wantWhy)
			if typer.calls != 0 {
				t.Errorf("typed %d key lines, want none", typer.calls)
			}
			if again := h.Resume(context.Background(), exec.Request{Command: preflightCmd()}); again.GetStatus() != tc.wantStatus {
				t.Errorf("Resume status = %v, want %v: resume must re-run the read", again.GetStatus(), tc.wantStatus)
			}
		})
	}
}

func TestHandlerRunReaderError(t *testing.T) {
	t.Parallel()
	h := Handler{Checker: &Checker{Reader: stubReader{err: errors.New("boom")}, Typer: &countTyper{}}}
	res := h.Run(context.Background(), exec.Request{Command: preflightCmd()})
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Errorf("status/reason = %v/%v, want FAILED/INTERNAL", res.GetStatus(), res.GetReason())
	}
}

func TestHandlerRunWrongKind(t *testing.T) {
	t.Parallel()
	h := Handler{Checker: &Checker{Reader: stubReader{facts: goodFacts()}}}
	res := h.Run(context.Background(), exec.Request{Command: &agentv1.Command{}})
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Errorf("status/reason = %v/%v, want FAILED/INTERNAL", res.GetStatus(), res.GetReason())
	}
}
