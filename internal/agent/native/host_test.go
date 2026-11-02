package native_test

import (
	"context"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// A full managed lifecycle through the host: start binds, send admits one
// turn, resume keeps the provider, interrupt stops the turn, stop releases.
// Every result carries ids and orders only; the adapter ran once per kind.
func TestHostFullLifecycleBindsAndResolves(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()

	start := startSession(t, f, startCmd("command-start"))
	got := start.GetStartNativeSession()
	if got.GetProviderSessionId() != "provider-session-1" {
		t.Fatalf("provider id %q, want the bound one", got.GetProviderSessionId())
	}
	if got.GetModel() != "opus" || got.GetEffort() != "high" {
		t.Fatalf("effective model/effort %+v", got)
	}

	send := sendCmd("command-send", "session-1", 1, "do the thing")
	loggedCommand(t, f.db, send)
	res := f.h.Run(ctx, send)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("send: status=%v reason=%v message=%q", res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if got := res.GetSendNativeTurn(); got.GetEffectId() != "command-send" || got.GetAdmissionOrder() != 2 {
		t.Fatalf("send detail %+v, want effect command-send order 2", got)
	}

	resume := resumeCmd("command-resume", "session-1", 1)
	loggedCommand(t, f.db, resume)
	res = f.h.Run(ctx, resume)
	if res.GetStatus() != typev1.Status_STATUS_OK || !res.GetResumeNativeSession().GetProviderSessionKept() {
		t.Fatalf("resume: %+v", res)
	}

	interrupt := interruptCmd("command-interrupt", "session-1", 1)
	loggedCommand(t, f.db, interrupt)
	res = f.h.Run(ctx, interrupt)
	if res.GetStatus() != typev1.Status_STATUS_OK || !res.GetInterruptNativeSession().GetAccepted() {
		t.Fatalf("interrupt: %+v", res)
	}

	stop := stopCmd("command-stop", "session-1", 1)
	loggedCommand(t, f.db, stop)
	res = f.h.Run(ctx, stop)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("stop: %+v", res)
	}
	if got := res.GetStopNativeSession(); !got.GetProcessExited() || !got.GetCleanupComplete() {
		t.Fatalf("stop detail %+v, want exit and cleanup", got)
	}

	want := []string{"start:session-1", "send:session-1", "resume:session-1", "interrupt:session-1", "stop:session-1"}
	if calls := f.fa.started(); !equal(calls, want) {
		t.Fatalf("adapter calls %v, want %v", calls, want)
	}
	if n := countEffects(t, f, "confirmed"); n != 5 {
		t.Fatalf("confirmed effects %d, want 5", n)
	}
}

// Validation fails before any admission: nothing is stored, no call runs,
// and a later valid start still works.
func TestHostValidationFailsClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		mutate func(*agentv1.Command)
		reason typev1.ReasonCode
	}{
		{"unknown backend", func(c *agentv1.Command) {
			c.GetStartNativeSession().Backend = typev1.NativeBackend(99)
		}, typev1.ReasonCode_REASON_CODE_NATIVE_BACKEND_UNSUPPORTED},
		{"unspecified backend", func(c *agentv1.Command) {
			c.GetStartNativeSession().Backend = typev1.NativeBackend_NATIVE_BACKEND_UNSPECIFIED
		}, typev1.ReasonCode_REASON_CODE_NATIVE_BACKEND_UNSUPPORTED},
		{"bad session id", func(c *agentv1.Command) {
			c.GetStartNativeSession().Session.Value = "HAS CAPS"
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"parent escape", func(c *agentv1.Command) {
			c.GetStartNativeSession().WorkdirSubpath = "../elsewhere"
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"absolute subpath", func(c *agentv1.Command) {
			c.GetStartNativeSession().WorkdirSubpath = "/etc"
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"oversized model", func(c *agentv1.Command) {
			c.GetStartNativeSession().Model = string(make([]byte, 129))
		}, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{
			"oversized input", func(c *agentv1.Command) {},
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.name == "oversized input" {
				send := sendCmd("command-big", "session-1", 1, string(make([]byte, 33<<10)))
				loggedCommand(t, f.db, send)
				res := f.h.Run(context.Background(), send)
				if res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
					t.Fatalf("reason %v, want INVALID_ARGUMENT", res.GetReason())
				}
				return
			}
			cmd := startCmd("command-bad")
			tc.mutate(cmd)
			loggedCommand(t, f.db, cmd)
			res := f.h.Run(context.Background(), cmd)
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("status=%v reason=%v, want FAILED %v", res.GetStatus(), res.GetReason(), tc.reason)
			}
			if calls := f.fa.started(); len(calls) != 0 {
				t.Fatalf("adapter ran on invalid input: %v", calls)
			}
		})
	}
}

// A second start for a bound session is ALREADY_MANAGED, never a second
// provider session; an unknown session's send is SESSION_NOT_FOUND.
func TestHostStartTwiceAndSendUnknown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	startSession(t, f, startCmd("command-start"))

	again := startCmd("command-start-2")
	loggedCommand(t, f.db, again)
	res := f.h.Run(ctx, again)
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_NATIVE_ALREADY_MANAGED {
		t.Fatalf("second start reason %v, want ALREADY_MANAGED", res.GetReason())
	}

	send := sendCmd("command-ghost", "ghost-session", 1, "hello")
	loggedCommand(t, f.db, send)
	res = f.h.Run(ctx, send)
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND {
		t.Fatalf("ghost send reason %v, want SESSION_NOT_FOUND", res.GetReason())
	}

	stale := sendCmd("command-stale", "session-1", 0, "hello")
	loggedCommand(t, f.db, stale)
	res = f.h.Run(ctx, stale)
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_NATIVE_GENERATION_STALE {
		t.Fatalf("stale send reason %v, want GENERATION_STALE", res.GetReason())
	}

	if calls := f.fa.started(); len(calls) != 1 {
		t.Fatalf("adapter calls %v, want exactly the first start", calls)
	}
}

// With no qualified adapter every command is NOT_QUALIFIED and nothing is
// advertised: unimplemented adapters stay unadvertised, and the journal is
// untouched.
func TestHostWithoutAdaptersIsNotQualified(t *testing.T) {
	t.Parallel()
	f := newBareFixture(t)
	ctx := context.Background()

	cmd := startCmd("command-start")
	loggedCommand(t, f.db, cmd)
	res := f.h.Run(ctx, cmd)
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_NATIVE_NOT_QUALIFIED {
		t.Fatalf("reason %v, want NOT_QUALIFIED", res.GetReason())
	}
	if caps := f.h.Advertised(); len(caps) != 0 {
		t.Fatalf("advertised %+v, want nothing: unimplemented adapters stay unadvertised", caps)
	}
	if n := countEffects(t, f, ""); n != 0 {
		t.Fatalf("journal holds %d effects after a refused start", n)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
