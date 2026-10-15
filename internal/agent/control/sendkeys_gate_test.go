package control

// Full-path SendKeys gate tests: the readScreen contract and whole-gate
// proofs with scripted pane reads (fakeRunner) instead of a live server.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

func TestReadScreen(t *testing.T) {
	t.Parallel()
	o := testSendOptions()
	stub := &stubSender{styledLines: []string{"a", "b"}}
	o.Sender = stub
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	text, res := e.readScreen(t.Context(), "%3")
	if res != nil {
		t.Fatalf("readScreen refused a whole capture: %v", res)
	}
	if text != "a\nb" {
		t.Fatalf("readScreen = %q, want %q", text, "a\nb")
	}
	stub.styledTruncated = true
	_, res = e.readScreen(t.Context(), "%3")
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_READY_TIMEOUT || !res.GetRetryable() {
		t.Fatalf("readScreen cut short = (%v, retryable %v), want (READY_TIMEOUT, true)",
			res.GetReason(), res.GetRetryable())
	}
	stub.styledTruncated = false
	stub.styledErr = errors.New("capture exploded")
	_, res = e.readScreen(t.Context(), "%3")
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("readScreen error = %v, want INTERNAL", res.GetReason())
	}
}

// TestReadScreenKeepsStyling pins the thread-1 contract: the gate reads the
// styled screen, escapes included, because the Claude placeholder check
// needs the faint attribute a plain read strips.
func TestReadScreenKeepsStyling(t *testing.T) {
	t.Parallel()
	o := testSendOptions()
	stub := &stubSender{styledLines: []string{"❯ \x1b[2mTry \"fix it\"\x1b[0m"}}
	o.Sender = stub
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	text, res := e.readScreen(t.Context(), "%3")
	if res != nil {
		t.Fatalf("readScreen refused a whole capture: %v", res)
	}
	if !strings.Contains(text, "\x1b[2m") {
		t.Fatalf("readScreen stripped styling: %q", text)
	}
}

// fakeRunner serves scripted one-shot tmux outputs for the SendKeys gate:
// the server start id for display-message and one pane row for list-panes.
// Anything else is an error, so a test proves which tmux calls the gate
// made.
type fakeRunner struct {
	startRow string
	panesRow string
}

func (f *fakeRunner) Run(_ context.Context, sp proc.Spec) (proc.Result, error) {
	args := strings.Join(sp.Args, " ")
	switch {
	case strings.Contains(args, "display-message"):
		return proc.Result{Stdout: []byte(f.startRow)}, nil
	case strings.Contains(args, "list-panes"):
		return proc.Result{Stdout: []byte(f.panesRow)}, nil
	default:
		return proc.Result{}, errors.New("fake runner refuses " + args)
	}
}

// gateOptions builds an Ensurer whose pane reads are scripted: pane %1 runs
// command, the start id derives from pid 1234 at 1700000000, the input
// check sees no key, and the sender serves the styled screen. It returns
// the options, the start id a command must carry, and the stub sender.
func gateOptions(command string, styled []string) (Options, string, *stubSender) {
	o := testSendOptions()
	o.Runner = &fakeRunner{startRow: "1234 1700000000\n", panesRow: "%1 0 " + command + "\n"}
	stub := &stubSender{styledLines: styled}
	o.Sender = stub
	return o, startID(1234, 1700000000), stub
}

// TestSendKeysStyledPlaceholderPasses is the thread-1 regression: a normal
// Claude prompt carries a faint placeholder, which only the styled read
// proves empty. A plain read of the same screen mistakes it for typed
// text and refuses PANE_BUSY.
func TestSendKeysStyledPlaceholderPasses(t *testing.T) {
	t.Parallel()
	o, start, stub := gateOptions("claude", []string{
		"Claude Code v2.1.296",
		"❯ \x1b[2mTry \"fix typecheck errors\"\x1b[0m",
	})
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	cmd := sendCmd()
	cmd.Pane.PaneId = "%1"
	cmd.Pane.ServerStartId = start
	res := e.SendKeys(t.Context(), cmd)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("SendKeys = (%v, %v), want OK: %q", res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(stub.sent) != 1 {
		t.Fatalf("SendKeys typed %d lines, want 1", len(stub.sent))
	}
}

// TestSendKeysOpenCodeDeferred pins the thread-3 contract: an OpenCode pane
// reads PANE_BUSY, retryable, with nothing typed. OpenCode takes no probe
// session and has no ready parser, so no screen of it is proven ready; its
// OPERATOR_TEXT waits for row 8's parser.
func TestSendKeysOpenCodeDeferred(t *testing.T) {
	t.Parallel()
	o, start, stub := gateOptions("opencode", []string{"anything"})
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	cmd := sendCmd()
	cmd.Pane.PaneId = "%1"
	cmd.Pane.ServerStartId = start
	res := e.SendKeys(t.Context(), cmd)
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_PANE_BUSY {
		t.Fatalf("SendKeys = (%v, %v), want (FAILED, PANE_BUSY): %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if !res.GetRetryable() {
		t.Fatalf("opencode refusal retryable = false, want true: %q", res.GetMessage())
	}
	if len(stub.sent) != 0 {
		t.Fatalf("a refused SendKeys typed %d lines", len(stub.sent))
	}
}

// TestSendKeysSamePaneSerializesCheckAndSend is the thread-2 regression:
// the executor runs each command on its own goroutine, so two OPERATOR_TEXT
// commands to one pane must not both pass the gate before either types.
// The first gate waits for the second (a rendezvous): overlapped gates log
// capture, capture, while serialized ones log capture, send, capture, send.
func TestSendKeysSamePaneSerializesCheckAndSend(t *testing.T) {
	// No t.Parallel: the rendezvous is timing-shaped on purpose.
	o, start, stub := gateOptions("codex", []string{"whatever", "› Ask Codex to do anything"})
	var mu sync.Mutex
	var events []string
	arrivals := 0
	rendezvous := make(chan struct{})
	stub.captureHook = func() {
		mu.Lock()
		events = append(events, "capture")
		arrivals++
		n := arrivals
		if n == 2 {
			close(rendezvous)
		}
		mu.Unlock()
		if n == 1 {
			select {
			case <-rendezvous:
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	stub.sendHook = func(l typing.Line) {
		cmd, err := l.Command()
		if err != nil {
			t.Errorf("sendHook Command: %v", err)
			return
		}
		mu.Lock()
		events = append(events, "send "+cmd)
		mu.Unlock()
	}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	lit := func(text string) *agentv1.SendKeys {
		cmd := sendCmd()
		cmd.Pane.PaneId = "%1"
		cmd.Pane.ServerStartId = start
		cmd.Keys = []*typev1.KeyInput{{Input: &typev1.KeyInput_Literal{Literal: text}}}
		return cmd
	}
	var wg sync.WaitGroup
	results := make([]*agentv1.CommandResult, 2)
	texts := []string{"alpha", "beta"}
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0] = e.SendKeys(context.Background(), lit(texts[0]))
	}()
	// Start the second command once the first is inside its gate, so the
	// overlap is forced rather than scheduled by luck.
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := arrivals
		mu.Unlock()
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first command never reached its gate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[1] = e.SendKeys(context.Background(), lit(texts[1]))
	}()
	wg.Wait()
	for i, res := range results {
		if res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("SendKeys %d = (%v, %v), want OK: %q", i, res.GetStatus(), res.GetReason(), res.GetMessage())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 4 || events[0] != "capture" || events[2] != "capture" ||
		!strings.HasPrefix(events[1], "send ") || !strings.HasPrefix(events[3], "send ") {
		t.Fatalf("gates overlapped: events = %q, want capture, send, capture, send", events)
	}
	if events[1] == events[3] {
		t.Fatalf("one command's line typed twice: events = %q", events)
	}
}
