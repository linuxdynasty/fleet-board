package control

// F8 RunHarnessCommand contract, shape, catalog and gate tests. The pane
// reads are scripted (fakeRunner): a refusal proves the gate, not the rig,
// typed nothing.

import (
	"strings"
	"testing"
	"unicode/utf8"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
)

// runHarnessCmd builds an OBSERVE /status for pane %1 with a placeholder
// start id; tests set the pane and the hash the case needs.
func runHarnessCmd() *agentv1.RunHarnessCommand {
	return &agentv1.RunHarnessCommand{
		Pane: &typev1.PaneKey{
			Server:        "default",
			ServerStartId: "live-start-id",
			PaneId:        "%1",
		},
		Command:       "/status",
		ExpectedClass: typev1.CommandClass_COMMAND_CLASS_OBSERVE,
		CatalogHash:   CatalogHash("codex"),
	}
}

// TestRunHarnessHandlerContract pins the executor seam for F8: the kind
// the handler runs, the capability it advertises for Command.requires, and
// the restart class per command class. Typing a slash command is not
// idempotent in general, so only the read-only OBSERVE class re-runs: a
// replayed OBSERVE runs again, every other class ends UNKNOWN_OUTCOME on
// resume and never retypes.
func TestRunHarnessHandlerContract(t *testing.T) {
	t.Parallel()
	e, err := New(testSendOptions())
	if err != nil {
		t.Fatal(err)
	}
	h := e.RunHarnessHandler()
	if h.Kind() != "RunHarnessCommand" {
		t.Fatalf("Kind = %q, want RunHarnessCommand (F8)", h.Kind())
	}
	caps := h.Capabilities()
	want := map[string]uint32{"tmux.control": 1, "tmux.default_server.control": 1, "harness.catalog": 1}
	if len(caps) != len(want) {
		t.Fatalf("Capabilities = %v, want %v for F8", caps, want)
	}
	for _, c := range caps {
		if want[c.GetName()] != c.GetVersion() || c.GetVersion() != 1 {
			t.Fatalf("Capabilities = %v, want %v for F8", caps, want)
		}
	}
	observe := &agentv1.Command{Kind: &agentv1.Command_RunHarnessCommand{RunHarnessCommand: runHarnessCmd()}}
	if got := h.Class(observe); got != exec.RepeatSafe {
		t.Fatalf("Class OBSERVE = %v, want RepeatSafe for F8", got)
	}
	for _, class := range []typev1.CommandClass{
		typev1.CommandClass_COMMAND_CLASS_SESSION,
		typev1.CommandClass_COMMAND_CLASS_GOAL,
		typev1.CommandClass_COMMAND_CLASS_REVIEW,
		typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
	} {
		cmd := runHarnessCmd()
		cmd.ExpectedClass = class
		c := &agentv1.Command{Kind: &agentv1.Command_RunHarnessCommand{RunHarnessCommand: cmd}}
		if got := h.Class(c); got != exec.NotRepeatSafe {
			t.Fatalf("Class %v = %v, want NotRepeatSafe for F8", class, got)
		}
		res := h.Resume(t.Context(), exec.Request{Command: c})
		if res.GetStatus() != typev1.Status_STATUS_UNKNOWN_OUTCOME {
			t.Fatalf("Resume %v = %v, want UNKNOWN_OUTCOME (never retype)", class, res.GetStatus())
		}
	}
}

func TestRunHarnessShapeRefusals(t *testing.T) {
	t.Parallel()
	tooLong := make([]byte, 4097)
	for i := range tooLong {
		tooLong[i] = 'x'
	}
	for _, tc := range []struct {
		name   string
		change func(*agentv1.RunHarnessCommand)
		status typev1.Status
		reason typev1.ReasonCode
	}{
		{"no command", func(c *agentv1.RunHarnessCommand) { c.Command = "" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"no slash", func(c *agentv1.RunHarnessCommand) { c.Command = "status" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"slash only", func(c *agentv1.RunHarnessCommand) { c.Command = "/" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"no pane", func(c *agentv1.RunHarnessCommand) { c.Pane = nil }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"wrong server", func(c *agentv1.RunHarnessCommand) { c.Pane.Server = "fleet" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"bad pane id", func(c *agentv1.RunHarnessCommand) { c.Pane.PaneId = "1" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"no start id", func(c *agentv1.RunHarnessCommand) { c.Pane.ServerStartId = "" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"class unset", func(c *agentv1.RunHarnessCommand) { c.ExpectedClass = typev1.CommandClass_COMMAND_CLASS_UNSPECIFIED }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"class deny", func(c *agentv1.RunHarnessCommand) { c.ExpectedClass = typev1.CommandClass_COMMAND_CLASS_DENY }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"destructive with args", func(c *agentv1.RunHarnessCommand) {
			c.Command = "/clear"
			c.ExpectedClass = typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE
			c.Args = "now"
			c.CatalogHash = CatalogHash("codex")
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"args too long", func(c *agentv1.RunHarnessCommand) { c.Args = string(tooLong) }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, err := New(testSendOptions())
			if err != nil {
				t.Fatal(err)
			}
			cmd := runHarnessCmd()
			tc.change(cmd)
			res := e.RunHarnessCommand(t.Context(), cmd)
			if res.GetStatus() != tc.status || res.GetReason() != tc.reason {
				t.Fatalf("RunHarnessCommand = (%v, %v), want (%v, %v): %q",
					res.GetStatus(), res.GetReason(), tc.status, tc.reason, res.GetMessage())
			}
		})
	}
}

// TestRunHarnessCatalogRefusals pins the allow-list: an unknown command is
// not in the catalog, a wrong expected class fails the mismatch, and a
// hash that is not the agent's is stale. Nothing is typed in any case.
func TestRunHarnessCatalogRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*agentv1.RunHarnessCommand)
		reason typev1.ReasonCode
	}{
		{"unknown command", func(c *agentv1.RunHarnessCommand) { c.Command = "/login" }, typev1.ReasonCode_REASON_CODE_COMMAND_NOT_IN_CATALOG},
		{"trailing text is another name", func(c *agentv1.RunHarnessCommand) { c.Command = "/status now" }, typev1.ReasonCode_REASON_CODE_COMMAND_NOT_IN_CATALOG},
		{"class mismatch", func(c *agentv1.RunHarnessCommand) { c.ExpectedClass = typev1.CommandClass_COMMAND_CLASS_SESSION }, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"empty hash", func(c *agentv1.RunHarnessCommand) { c.CatalogHash = "" }, typev1.ReasonCode_REASON_CODE_CATALOG_STALE},
		{"wrong hash", func(c *agentv1.RunHarnessCommand) { c.CatalogHash = "0" }, typev1.ReasonCode_REASON_CODE_CATALOG_STALE},
		{"one bit off the agent hash", func(c *agentv1.RunHarnessCommand) {
			h := CatalogHash("codex")
			last := '0'
			if h[len(h)-1] == '0' {
				last = '1'
			}
			c.CatalogHash = h[:len(h)-1] + string(last)
		}, typev1.ReasonCode_REASON_CODE_CATALOG_STALE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, start, stub := gateOptions("codex", []string{"whatever", "› Ask Codex to do anything"})
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			cmd := runHarnessCmd()
			cmd.Pane.PaneId = "%1"
			cmd.Pane.ServerStartId = start
			tc.change(cmd)
			res := e.RunHarnessCommand(t.Context(), cmd)
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("RunHarnessCommand = (%v, %v), want (FAILED, %v): %q",
					res.GetStatus(), res.GetReason(), tc.reason, res.GetMessage())
			}
			if len(stub.sent) != 0 {
				t.Fatalf("a refused RunHarnessCommand typed %d lines", len(stub.sent))
			}
		})
	}
}

// TestRunHarnessObserveHappyPath proves the F8 allow-list end to end on a
// scripted idle prompt: /status types as one literal plus Enter, and the
// OBSERVE result carries the class and the stripped follow-up screen. The
// scripted screens redraw after Enter: the first follow-up read still shows
// the old prompt, then the answer lands and repeats, so the settle wait
// proves it never records the pre-send screen.
func TestRunHarnessObserveHappyPath(t *testing.T) {
	t.Parallel()
	prompt := []string{"whatever", "› Ask Codex to do anything"}
	settled := []string{"whatever", "status: ok", "› Ask Codex to do anything"}
	o, start, stub := gateOptions("codex", prompt)
	stub.seq = [][]string{prompt, prompt, settled, settled}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	cmd := runHarnessCmd()
	cmd.Pane.PaneId = "%1"
	cmd.Pane.ServerStartId = start
	res := e.RunHarnessCommand(t.Context(), cmd)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("RunHarnessCommand = (%v, %v), want OK: %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(stub.sent) != 2 {
		t.Fatalf("RunHarnessCommand typed %d lines, want the command plus Enter", len(stub.sent))
	}
	detail, ok := res.GetDetail().(*agentv1.CommandResult_RunHarnessCommand)
	if !ok || detail.RunHarnessCommand == nil {
		t.Fatalf("RunHarnessCommand detail = %T, want the F8 result", res.GetDetail())
	}
	if got := detail.RunHarnessCommand.GetClass(); got != typev1.CommandClass_COMMAND_CLASS_OBSERVE {
		t.Fatalf("result class = %v, want OBSERVE", got)
	}
	screen := detail.RunHarnessCommand.GetScreen()
	if screen == "" || !strings.Contains(screen, "status: ok") {
		t.Fatalf("result screen = %q, want the settled follow-up screen", screen)
	}
	if strings.Contains(screen, "\x1b") {
		t.Fatalf("result screen keeps escape bytes: %q", screen)
	}
}

// TestRunHarnessObserveStaleNeverSettles is the P1 regression: a harness
// that never redraws after Enter must not record the old prompt as the
// settled result. The command ends READY_TIMEOUT, retryable, so the hub may
// ask again instead of keeping stale output as success.
func TestRunHarnessObserveStaleNeverSettles(t *testing.T) {
	t.Parallel()
	prompt := []string{"whatever", "› Ask Codex to do anything"}
	o, start, stub := gateOptions("codex", prompt)
	stub.seq = [][]string{prompt}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	cmd := runHarnessCmd()
	cmd.Pane.PaneId = "%1"
	cmd.Pane.ServerStartId = start
	res := e.RunHarnessCommand(t.Context(), cmd)
	if res.GetStatus() != typev1.Status_STATUS_FAILED ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_READY_TIMEOUT {
		t.Fatalf("RunHarnessCommand = (%v, %v), want (FAILED, READY_TIMEOUT) for a stale screen: %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if !res.GetRetryable() {
		t.Fatalf("stale settle retryable = false, want true: %q", res.GetMessage())
	}
	if detail, ok := res.GetDetail().(*agentv1.CommandResult_RunHarnessCommand); ok && detail.RunHarnessCommand != nil &&
		detail.RunHarnessCommand.GetScreen() != "" {
		t.Fatalf("stale settle attached screen %q, want none", detail.RunHarnessCommand.GetScreen())
	}
}

// TestRunHarnessObserveFullArgsChunked is the 4 KiB P2 regression: a full
// 4096-byte argument plus the command text exceeds one typing literal, so
// it types as two bounded literals plus Enter under the same pane lock and
// still settles. The command is valid input the agent must not refuse.
func TestRunHarnessObserveFullArgsChunked(t *testing.T) {
	t.Parallel()
	prompt := []string{"whatever", "› Ask Codex to do anything"}
	settled := []string{"whatever", "args seen", "› Ask Codex to do anything"}
	o, start, stub := gateOptions("codex", prompt)
	stub.seq = [][]string{prompt, prompt, settled, settled}
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	cmd := runHarnessCmd()
	cmd.Pane.PaneId = "%1"
	cmd.Pane.ServerStartId = start
	cmd.Args = strings.Repeat("x", 4096)
	res := e.RunHarnessCommand(t.Context(), cmd)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("RunHarnessCommand = (%v, %v), want OK for full 4 KiB args: %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(stub.sent) != 3 {
		t.Fatalf("RunHarnessCommand typed %d lines, want two literals plus Enter for full args", len(stub.sent))
	}
	detail, ok := res.GetDetail().(*agentv1.CommandResult_RunHarnessCommand)
	if !ok || detail.RunHarnessCommand == nil {
		t.Fatalf("RunHarnessCommand detail = %T, want the F8 result", res.GetDetail())
	}
	if screen := detail.RunHarnessCommand.GetScreen(); !strings.Contains(screen, "args seen") {
		t.Fatalf("result screen = %q, want the settled follow-up screen", screen)
	}
}

// TestSplitLiteralKeepsUTF8Boundaries proves the chunker never cuts a
// multi-byte rune: every piece stays valid UTF-8, fits the typing cap, and
// joins back to the input.
func TestSplitLiteralKeepsUTF8Boundaries(t *testing.T) {
	t.Parallel()
	text := strings.Repeat("é", 2000) + strings.Repeat("x", 500)
	chunks := splitLiteral(text, typing.MaxLiteralBytes)
	if got := strings.Join(chunks, ""); got != text {
		t.Fatal("splitLiteral did not join back to the input")
	}
	for i, c := range chunks {
		if len(c) > typing.MaxLiteralBytes {
			t.Fatalf("chunk %d holds %d bytes, want at most the typing cap", i, len(c))
		}
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
	}
	if len(chunks) < 2 {
		t.Fatalf("splitLiteral returned %d chunks for %d bytes, want more than one", len(chunks), len(text))
	}
}

// TestRunHarnessDestructiveHappyPath proves a DESTRUCTIVE command types
// with no args and attaches no screen: the class, not the screen, is the
// result. The local approval it waits on is the executor's, already proven
// there; this handler only types once every check passes.
func TestRunHarnessDestructiveHappyPath(t *testing.T) {
	t.Parallel()
	o, start, stub := gateOptions("codex", []string{"whatever", "› Ask Codex to do anything"})
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	cmd := runHarnessCmd()
	cmd.Pane.PaneId = "%1"
	cmd.Pane.ServerStartId = start
	cmd.Command = "/clear"
	cmd.ExpectedClass = typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE
	cmd.CatalogHash = CatalogHash("codex")
	res := e.RunHarnessCommand(t.Context(), cmd)
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("RunHarnessCommand = (%v, %v), want OK: %q",
			res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if len(stub.sent) != 2 {
		t.Fatalf("RunHarnessCommand typed %d lines, want the command plus Enter", len(stub.sent))
	}
	detail, ok := res.GetDetail().(*agentv1.CommandResult_RunHarnessCommand)
	if !ok || detail.RunHarnessCommand == nil {
		t.Fatalf("RunHarnessCommand detail = %T, want the F8 result", res.GetDetail())
	}
	if got := detail.RunHarnessCommand.GetClass(); got != typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE {
		t.Fatalf("result class = %v, want DESTRUCTIVE", got)
	}
	if screen := detail.RunHarnessCommand.GetScreen(); screen != "" {
		t.Fatalf("result screen = %q, want none for a DESTRUCTIVE command", screen)
	}
}

// TestRunHarnessGateRefusals proves each live check refuses with its reason
// and types nothing: a trust screen, a startup screen, a drafted input
// line, a shell pane, a Cursor pane, and a pane the developer just typed
// in.
func TestRunHarnessGateRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		command string
		screen  []string
		reason  typev1.ReasonCode
	}{
		{"trust screen", "codex", []string{
			"  Folder access",
			"  Trust this folder? Codex can read, edit, and run files here.",
			"› 1. Trust and continue",
			"  2. Quit",
		}, typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED},
		{"startup screen", "codex", []string{
			"  Background server has incompatible feature settings",
			"  1. Run without daemon this time",
		}, typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN},
		{"draft input line", "codex", []string{
			"whatever",
			"› half a thought",
		}, typev1.ReasonCode_REASON_CODE_PANE_BUSY},
		{"shell pane", "sh", []string{"whatever", "› Ask Codex to do anything"}, typev1.ReasonCode_REASON_CODE_PANE_BUSY},
		{"cursor pane", "cursor-agent", []string{"whatever", "› Ask Codex to do anything"}, typev1.ReasonCode_REASON_CODE_PANE_BUSY},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o, start, stub := gateOptions(tc.command, tc.screen)
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			cmd := runHarnessCmd()
			cmd.Pane.PaneId = "%1"
			cmd.Pane.ServerStartId = start
			res := e.RunHarnessCommand(t.Context(), cmd)
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != tc.reason {
				t.Fatalf("RunHarnessCommand = (%v, %v), want (FAILED, %v): %q",
					res.GetStatus(), res.GetReason(), tc.reason, res.GetMessage())
			}
			if len(stub.sent) != 0 {
				t.Fatalf("a refused RunHarnessCommand typed %d lines", len(stub.sent))
			}
		})
	}
}
