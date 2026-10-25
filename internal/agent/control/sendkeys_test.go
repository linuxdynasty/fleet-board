package control

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
)

// stubSender stands in for the observer client: it records every line and
// serves a scripted screen. failAt fails the 1-based Send call of that
// number (0 never fails), for the partial-send test; failErr is the error
// it fails with (a tmux.ErrMaybeApplied stands in for a lost reply).
type stubSender struct {
	// mu guards sent and the capture sequence: one stub serves concurrent
	// commands in the serialization test. Hooks run outside it; tests
	// guard their own state.
	mu              sync.Mutex
	sent            []typing.Line
	styledLines     []string
	styledTruncated bool
	styledErr       error
	failAt          int
	failErr         error
	// seq, when set, scripts one screen per CaptureStyled call in order;
	// the last entry repeats. It models a harness that redraws after
	// Enter, so the settle wait can prove it never records the pre-send
	// screen. When empty the stub serves styledLines every time.
	seq      [][]string
	seqCalls int
	// captureHook runs on every CaptureStyled, sendHook on every Send.
	captureHook func()
	sendHook    func(typing.Line)
}

var errStubSend = errors.New("stub sender refused the line")

func (s *stubSender) Send(_ context.Context, l typing.Line) error {
	s.mu.Lock()
	s.sent = append(s.sent, l)
	n := len(s.sent)
	s.mu.Unlock()
	if s.sendHook != nil {
		s.sendHook(l)
	}
	if s.failAt > 0 && n == s.failAt {
		if s.failErr != nil {
			return s.failErr
		}
		return errStubSend
	}
	return nil
}

func (s *stubSender) CaptureStyled(context.Context, tmux.PaneID) (tmux.Capture, error) {
	if s.captureHook != nil {
		s.captureHook()
	}
	if s.styledErr != nil {
		return tmux.Capture{}, s.styledErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seq) > 0 {
		i := s.seqCalls
		if i >= len(s.seq) {
			i = len(s.seq) - 1
		}
		s.seqCalls++
		return tmux.Capture{Lines: s.seq[i], Truncated: s.styledTruncated}, nil
	}
	return tmux.Capture{Lines: s.styledLines, Truncated: s.styledTruncated}, nil
}

// stubCheckInput reports no key ever: the gate tests set their own clock
// and input time through Options.
func stubCheckInput(context.Context, string) (time.Time, error) { return time.Time{}, nil }

// testSendOptions builds an Ensurer whose seams never fire: the shape
// refusals fail before any tmux, input or screen read.
func testSendOptions() Options { return testOptions() }

// sendCmd builds an OPERATOR_TEXT SendKeys for pane %1 with one literal.
func sendCmd() *agentv1.SendKeys {
	return &agentv1.SendKeys{
		Pane: &typev1.PaneKey{
			Server:        "default",
			ServerStartId: "live-start-id",
			PaneId:        "%1",
		},
		Purpose: typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_OPERATOR_TEXT,
		Keys: []*typev1.KeyInput{
			{Input: &typev1.KeyInput_Literal{Literal: "hi"}},
		},
	}
}

// TestSendKeysHandlerContract pins the executor seam for F3: the kind the
// handler runs, the capability it advertises for Command.requires, and the
// restart class. SendKeys is NOT idempotent, so a resume after a restart
// ends UNKNOWN_OUTCOME and never retypes (02-contracts).
func TestSendKeysHandlerContract(t *testing.T) {
	t.Parallel()
	e, err := New(testSendOptions())
	if err != nil {
		t.Fatal(err)
	}
	h := e.SendKeysHandler()
	if h.Kind() != "SendKeys" {
		t.Fatalf("Kind = %q, want SendKeys (F3)", h.Kind())
	}
	caps := h.Capabilities()
	want := map[string]uint32{"tmux.control": 1, "tmux.default_server.control": 1}
	if len(caps) != len(want) {
		t.Fatalf("Capabilities = %v, want %v for F3", caps, want)
	}
	for _, c := range caps {
		if want[c.GetName()] != c.GetVersion() || c.GetVersion() != 1 {
			t.Fatalf("Capabilities = %v, want %v for F3", caps, want)
		}
	}
	cmd := &agentv1.Command{Kind: &agentv1.Command_SendKeys{SendKeys: sendCmd()}}
	if got := h.Class(cmd); got != exec.NotRepeatSafe {
		t.Fatalf("Class = %v, want NotRepeatSafe for F3", got)
	}
	res := h.Resume(t.Context(), exec.Request{Command: cmd})
	if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
		t.Fatalf("Resume = %v, want UNKNOWN_OUTCOME (never retype)", res.GetStatus())
	}
}

func TestSendKeysShapeRefusals(t *testing.T) {
	t.Parallel()
	lit := func(s string) *typev1.KeyInput { return &typev1.KeyInput{Input: &typev1.KeyInput_Literal{Literal: s}} }
	key := func(k typev1.NamedKey) *typev1.KeyInput { return &typev1.KeyInput{Input: &typev1.KeyInput_Key{Key: k}} }
	for _, tc := range []struct {
		name   string
		change func(*agentv1.SendKeys)
		status typev1.Status
		reason typev1.ReasonCode
	}{
		{"unspecified purpose", func(c *agentv1.SendKeys) {
			c.Purpose = typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_UNSPECIFIED
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"prompt answer is a later slice", func(c *agentv1.SendKeys) {
			c.Purpose = typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_PROMPT_ANSWER
			c.AnsweringPrompt = &typev1.PromptId{Value: "p1"}
		}, typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_UNSPECIFIED},
		{"goal nudge is a later slice", func(c *agentv1.SendKeys) {
			c.Purpose = typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_GOAL_NUDGE
		}, typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_UNSPECIFIED},
		{"goal resume is a later slice", func(c *agentv1.SendKeys) {
			c.Purpose = typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_GOAL_RESUME
		}, typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_UNSPECIFIED},
		{"answering prompt with operator text", func(c *agentv1.SendKeys) {
			c.AnsweringPrompt = &typev1.PromptId{Value: "p1"}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"no keys", func(c *agentv1.SendKeys) {
			c.Keys = nil
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"no pane", func(c *agentv1.SendKeys) {
			c.Pane = nil
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"pane on another server", func(c *agentv1.SendKeys) {
			c.Pane.Server = "fleet"
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"malformed pane id", func(c *agentv1.SendKeys) {
			c.Pane.PaneId = "%x"
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"missing server start id", func(c *agentv1.SendKeys) {
			c.Pane.ServerStartId = ""
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"literal with a newline", func(c *agentv1.SendKeys) {
			c.Keys = []*typev1.KeyInput{lit("a\nb")}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"empty literal", func(c *agentv1.SendKeys) {
			c.Keys = []*typev1.KeyInput{lit("")}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"key outside the closed set", func(c *agentv1.SendKeys) {
			c.Keys = []*typev1.KeyInput{key(typev1.NamedKey(99))}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, err := New(testSendOptions())
			if err != nil {
				t.Fatal(err)
			}
			cmd := sendCmd()
			tc.change(cmd)
			res := e.SendKeys(t.Context(), cmd)
			if res.GetStatus() != tc.status || res.GetReason() != tc.reason {
				t.Fatalf("SendKeys = (%v, %v), want (%v, %v): %q",
					res.GetStatus(), res.GetReason(), tc.status, tc.reason, res.GetMessage())
			}
			if res.GetRetryable() {
				t.Fatalf("SendKeys retryable = true, want false for a shape refusal: %q", res.GetMessage())
			}
		})
	}
}

func TestClassifyForSend(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		harness string
		screen  string
		reason  typev1.ReasonCode
		retry   bool
	}{
		{"claude ready", "claude", "Claude Code v9.9.9\n❯ \n", typev1.ReasonCode_REASON_CODE_UNSPECIFIED, false},
		{"claude draft", "claude", "Claude Code v9.9.9\n❯ typed text\n", typev1.ReasonCode_REASON_CODE_PANE_BUSY, true},
		{
			"claude trust", "claude", "Accessing workspace:\nYes, I trust this folder\n",
			typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED, false,
		},
		{
			"claude startup", "claude", "Release notes\nSelect a version to view its notes.\n",
			typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN, false,
		},
		{"claude unknown", "claude", "some shell output\n$ \n", typev1.ReasonCode_REASON_CODE_READY_TIMEOUT, true},
		{"codex ready", "codex", "whatever\n› Ask Codex to do anything\n", typev1.ReasonCode_REASON_CODE_UNSPECIFIED, false},
		{"codex draft", "codex", "whatever\n› do the thing\n", typev1.ReasonCode_REASON_CODE_PANE_BUSY, true},
		{"muse ready", "muse", "Muse Code\n❯\n", typev1.ReasonCode_REASON_CODE_UNSPECIFIED, false},
		{"muse draft", "muse", "Muse Code\n❯ typed\n", typev1.ReasonCode_REASON_CODE_PANE_BUSY, true},
		{
			"cursor plain capture stays busy per row 30", "cursor", "→ Plan, search, build anything\n",
			typev1.ReasonCode_REASON_CODE_PANE_BUSY, true,
		},
		{
			"claude styled placeholder is ready", "claude", "Claude Code v9.9.9\n❯ \x1b[2mTry \"fix it\"\x1b[0m\n",
			typev1.ReasonCode_REASON_CODE_UNSPECIFIED, false,
		},
		{
			"cursor styled ready stays busy per row 30", "cursor", "Workspace\n\x1b[2m→ Plan, search, build anything\x1b[0m\n",
			typev1.ReasonCode_REASON_CODE_PANE_BUSY, true,
		},
		{"unprobed harness", "opencode", "anything\n", typev1.ReasonCode_REASON_CODE_READY_TIMEOUT, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := classifyForSend(tc.harness, tc.screen)
			if tc.reason == typev1.ReasonCode_REASON_CODE_UNSPECIFIED {
				if res != nil {
					t.Fatalf("classifyForSend refused a ready screen: %v", res)
				}
				return
			}
			if res == nil {
				t.Fatalf("classifyForSend passed a %s screen", tc.name)
			}
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("classifyForSend = (%v, %v), want (FAILED, %v): %q",
					res.GetStatus(), res.GetReason(), tc.reason, res.GetMessage())
			}
			if res.GetRetryable() != tc.retry {
				t.Fatalf("classifyForSend retryable = %v, want %v: %q", res.GetRetryable(), tc.retry, res.GetMessage())
			}
		})
	}
}

func TestHarnessCommands(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"claude", "codex", "muse", "cursor"} {
		found := false
		for _, name := range harnessCommands {
			found = found || name == h
		}
		if !found {
			t.Fatalf("harnessCommands maps no command to %q", h)
		}
	}
	for _, cmd := range []string{"sh", "bash", "sleep", "cat", "", "Claude"} {
		if _, ok := harnessCommands[cmd]; ok {
			t.Fatalf("harnessCommands maps %q: shells never take operator text in this slice", cmd)
		}
	}
}

func TestNamedKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   typev1.NamedKey
		want typing.Key
	}{
		{typev1.NamedKey_NAMED_KEY_ENTER, typing.KeyEnter},
		{typev1.NamedKey_NAMED_KEY_ESCAPE, typing.KeyEscape},
		{typev1.NamedKey_NAMED_KEY_TAB, typing.KeyTab},
		{typev1.NamedKey_NAMED_KEY_BACKSPACE, typing.KeyBackspace},
		{typev1.NamedKey_NAMED_KEY_UP, typing.KeyUp},
		{typev1.NamedKey_NAMED_KEY_DOWN, typing.KeyDown},
		{typev1.NamedKey_NAMED_KEY_LEFT, typing.KeyLeft},
		{typev1.NamedKey_NAMED_KEY_RIGHT, typing.KeyRight},
		{typev1.NamedKey_NAMED_KEY_CTRL_C, typing.KeyCtrlC},
	} {
		if got, ok := namedKey(tc.in); !ok || got != tc.want {
			t.Fatalf("namedKey(%v) = (%v, %v), want (%v, true)", tc.in, got, ok, tc.want)
		}
	}
	for _, in := range []typev1.NamedKey{typev1.NamedKey_NAMED_KEY_UNSPECIFIED, typev1.NamedKey(99)} {
		if got, ok := namedKey(in); ok {
			t.Fatalf("namedKey(%v) = (%v, true), want refused", in, got)
		}
	}
}

func testLines(t *testing.T) []typing.Line {
	t.Helper()
	pane := &typev1.PaneKey{Server: "default", ServerStartId: "abc-1", PaneId: "%3"}
	a, err := typing.NewLiteral(nil, pane, typing.PurposeOperatorText, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := typing.NewKeys(nil, pane, typing.PurposeOperatorText, typing.KeyEnter)
	if err != nil {
		t.Fatal(err)
	}
	return []typing.Line{a, b}
}

func TestSendLines(t *testing.T) {
	t.Parallel()
	stub := &stubSender{}
	o := testSendOptions()
	o.Sender = stub
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	res := e.sendLines(t.Context(), "%3", testLines(t))
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("sendLines = %v, want OK: %q", res.GetStatus(), res.GetMessage())
	}
	if len(stub.sent) != 2 {
		t.Fatalf("sendLines sent %d lines, want both in order", len(stub.sent))
	}
}

func TestSendLinesPartial(t *testing.T) {
	t.Parallel()
	o := testSendOptions()
	stub := &stubSender{failAt: 2}
	o.Sender = stub
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	res := e.sendLines(t.Context(), "%3", testLines(t))
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("sendLines = (%v, %v), want (FAILED, INTERNAL): %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(stub.sent) != 2 {
		t.Fatalf("sendLines attempted %d sends, want 2 (the failure stops it)", len(stub.sent))
	}
}

// TestSendLinesMaybeApplied pins the non-idempotent outcome: a send whose
// reply was lost (tmux.ErrMaybeApplied) ends UNKNOWN_OUTCOME, never a
// known failure, so the hub asks a person instead of retyping. A plain
// refusal stays FAILED/INTERNAL (TestSendLinesPartial).
func TestSendLinesMaybeApplied(t *testing.T) {
	t.Parallel()
	o := testSendOptions()
	stub := &stubSender{failAt: 2, failErr: tmux.ErrMaybeApplied}
	o.Sender = stub
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	res := e.sendLines(t.Context(), "%3", testLines(t))
	if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
		t.Fatalf("sendLines = %v, want UNKNOWN_OUTCOME: %q", res.GetStatus(), res.GetMessage())
	}
	if res.GetRetryable() {
		t.Fatalf("sendLines retryable = true, want false (the hub never retries SendKeys): %q", res.GetMessage())
	}
	if len(stub.sent) != 2 {
		t.Fatalf("sendLines attempted %d sends, want 2 (the failure stops it)", len(stub.sent))
	}
}
