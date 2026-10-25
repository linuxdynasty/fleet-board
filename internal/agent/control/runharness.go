package control

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
	"github.com/linuxdynasty/fleet-board/internal/harness"
	"github.com/linuxdynasty/fleet-board/internal/scrub"
)

// maxHarnessArgs is the 4 KiB argument cap of command_tmux.proto: arguments
// are never typed for DESTRUCTIVE or DENY classes.
const maxHarnessArgs = 4 * 1024

// observeScreenCap is the 64 KiB cap on the OBSERVE result screen.
const observeScreenCap = 64 * 1024

// observeSettleTimeout bounds the wait for the harness to redraw after
// Enter: an OBSERVE result must carry the screen after the command settled
// (command_tmux.proto), never the pre-send screen. observeSettlePoll is the
// gap between screen reads while waiting.
const (
	observeSettleTimeout = 3 * time.Second
	observeSettlePoll    = 50 * time.Millisecond
)

// runHarnessHandler is the exec.Handler for RunHarnessCommand. The wiring
// row registers one per server the agent serves, next to the session,
// window, send-keys, interrupt and respawn handlers.
type runHarnessHandler struct {
	e *Ensurer
}

// RunHarnessHandler returns the Ensurer as the executor's Handler for
// RunHarnessCommand.
func (e *Ensurer) RunHarnessHandler() exec.Handler { return runHarnessHandler{e: e} }

// Kind is the Command.kind arm's message name.
func (h runHarnessHandler) Kind() string { return "RunHarnessCommand" }

// Capabilities are advertised in Hello while the handler is registered:
// the tmux control set plus `harness.catalog` version 1, the capability
// 02e names for RunHarnessCommand, so the hub routes the kind here.
func (h runHarnessHandler) Capabilities() []*typev1.Capability {
	return append(capabilitiesFor(h.e.kind),
		&typev1.Capability{Name: "harness.catalog", Version: 1})
}

// Class re-runs only the read-only OBSERVE class from the start: typing a
// slash command is not idempotent in general, so a replayed or resumed
// SESSION, GOAL, REVIEW or DESTRUCTIVE command never retypes.
func (h runHarnessHandler) Class(c *agentv1.Command) exec.Class {
	cmd, ok := c.GetKind().(*agentv1.Command_RunHarnessCommand)
	if ok && cmd.RunHarnessCommand.GetExpectedClass() == typev1.CommandClass_COMMAND_CLASS_OBSERVE {
		return exec.RepeatSafe
	}
	return exec.NotRepeatSafe
}

// Run does the command and returns its result. The executor sets the id,
// the times and the trace, and scrubs the message.
func (h runHarnessHandler) Run(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	cmd, ok := r.Command.GetKind().(*agentv1.Command_RunHarnessCommand)
	if !ok || cmd.RunHarnessCommand == nil {
		return &agentv1.CommandResult{
			Status:  typev1.Status_STATUS_FAILED,
			Reason:  typev1.ReasonCode_REASON_CODE_INTERNAL,
			Message: "control: RunHarnessCommand handler got another kind",
		}
	}
	return h.e.RunHarnessCommand(ctx, cmd.RunHarnessCommand)
}

// Resume re-runs only OBSERVE from the start. Any other class ends
// UNKNOWN_OUTCOME without retyping: the hub asks a person whether the
// command landed instead of sending it again.
func (h runHarnessHandler) Resume(ctx context.Context, r exec.Request) *agentv1.CommandResult {
	if h.Class(r.Command) == exec.RepeatSafe {
		return h.Run(ctx, r)
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_UNKNOWN_OUTCOME,
		Message: "RunHarnessCommand is never retyped after a restart; ask whether the command landed",
	}
}

// RunHarnessCommand types one allow-listed slash command at an idle prompt
// (F8). The executor already checked the deadline, the epoch and seq, the
// safe mode, the rate limit, the role matrix and the local approval; this
// checks what only it can see: the call's shape, the pane, the catalog and
// the live screen. Every refusal types nothing.
func (e *Ensurer) RunHarnessCommand(ctx context.Context, cmd *agentv1.RunHarnessCommand) *agentv1.CommandResult {
	name := cmd.GetCommand()
	if len(name) < 2 || name[0] != '/' {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a harness command carries its leading slash")
	}
	class := cmd.GetExpectedClass()
	if class == typev1.CommandClass_COMMAND_CLASS_UNSPECIFIED ||
		class == typev1.CommandClass_COMMAND_CLASS_DENY {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a harness command states the class the hub believes")
	}
	if args := cmd.GetArgs(); args != "" &&
		class == typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a destructive command carries no args")
	}
	if len(cmd.GetArgs()) > maxHarnessArgs {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the arguments pass the 4 KiB cap")
	}
	pane := cmd.GetPane()
	if pane == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a pane key is required")
	}
	if pane.GetServer() != e.server {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"this ensurer does not serve that tmux server")
	}
	paneID := pane.GetPaneId()
	if !paneIDRE.MatchString(paneID) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a pane id looks like %3")
	}
	if pane.GetServerStartId() == "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a server start id is required")
	}
	// One pane's check-and-send is one critical section, shared with F3:
	// without it two commands to one pane could both read an empty line
	// and then interleave their typing.
	unlock := e.lockPane(paneID)
	defer unlock()
	start, err := e.startID(ctx)
	if err != nil {
		return e.down(err)
	}
	if pane.GetServerStartId() != start {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND,
			"that pane belongs to an older server")
	}
	dead, command, res := e.paneInfo(ctx, paneID)
	if res != nil {
		return res
	}
	if dead {
		return refuseRetryable(typev1.ReasonCode_REASON_CODE_PANE_BUSY, "the pane is dead; nothing typed")
	}
	// Nothing while the developer types (02h): the check reads
	// list-clients at this moment, never stored push state. It runs before
	// the harness read, like F3, so a person typing wins over every other
	// gate.
	at, err := e.checkInput(ctx, paneID)
	if err != nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the developer-input check failed")
	}
	if !at.IsZero() && e.now().Sub(at) < inputHoldoff {
		return refuseRetryable(typev1.ReasonCode_REASON_CODE_PANE_BUSY,
			"a person typed in this pane recently; nothing typed")
	}
	harnessName, ok := harnessCommands[command]
	if !ok {
		return refuseRetryable(typev1.ReasonCode_REASON_CODE_PANE_BUSY,
			"this slice types into harness panes only, never shells")
	}
	if harnessName == "cursor" {
		// Row 30: until the styled-input rule is proven, every
		// RunHarnessCommand on a Cursor pane answers PANE_BUSY.
		return refuseRetryable(typev1.ReasonCode_REASON_CODE_PANE_BUSY,
			"a Cursor pane never proves its input line empty in this slice")
	}
	if !inCatalog(harnessName, name) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_COMMAND_NOT_IN_CATALOG,
			"that command is not in the harness catalog; nothing typed")
	}
	if got := catalogClass(harnessName, name); got != class {
		if got == typev1.CommandClass_COMMAND_CLASS_DENY {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_COMMAND_CLASS_DENIED,
				"deny-class commands are never typed")
		}
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the hub's class is not the catalog's class for that command")
	}
	if h := cmd.GetCatalogHash(); h == "" || h != CatalogHash(harnessName) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_CATALOG_STALE,
			"that catalog hash is not the agent's; the hub sends the hash the snapshot reports")
	}
	screen, res := e.readScreen(ctx, paneID)
	if res != nil {
		return res
	}
	if res := classifyForSend(harnessName, screen); res != nil {
		return res
	}
	text := name
	if args := cmd.GetArgs(); args != "" {
		text += " " + args
	}
	lines := make([]typing.Line, 0, 2)
	for _, chunk := range splitLiteral(text, typing.MaxLiteralBytes) {
		lit, err := typing.NewLiteral(e.rec, pane, typing.PurposeHarnessCommand, chunk)
		if err != nil {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				fmt.Sprintf("that command is not typeable: %s", err.Error()))
		}
		lines = append(lines, lit)
	}
	enter, err := typing.NewKeys(e.rec, pane, typing.PurposeHarnessCommand, typing.KeyEnter)
	if err != nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the Enter key is not typeable")
	}
	lines = append(lines, enter)
	if res := e.sendLines(ctx, paneID, lines); res.GetStatus() != typev1.Status_STATUS_OK {
		return res
	}
	result := &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Message: fmt.Sprintf("typed %s into %s", name, paneID),
		Detail: &agentv1.CommandResult_RunHarnessCommand{
			RunHarnessCommand: &agentv1.RunHarnessCommandResult{Class: class},
		},
	}
	if class != typev1.CommandClass_COMMAND_CLASS_OBSERVE {
		return result
	}
	settled, res := e.waitForSettledScreen(ctx, paneID, screen)
	if res != nil {
		return res
	}
	detail, ok := result.GetDetail().(*agentv1.CommandResult_RunHarnessCommand)
	if !ok || detail.RunHarnessCommand == nil {
		result.Message += "; the follow-up screen read failed, so no screen is attached"
		return result
	}
	screen, _ = scrub.Bound(cleanScreen(settled), observeScreenCap)
	detail.RunHarnessCommand.Screen = screen
	return result
}

// waitForSettledScreen waits for the harness to redraw after Enter and
// returns the settled screen. It polls the pane until two back-to-back
// reads agree and differ from the pre-send screen, so a fast capture of the
// old ready prompt is never recorded as the result. A screen that never
// changes reads READY_TIMEOUT, retryable: OBSERVE is RepeatSafe, so the hub
// may ask again rather than keep a stale screen. A failed follow-up read
// returns its refusal instead of silent success with no screen.
func (e *Ensurer) waitForSettledScreen(ctx context.Context, paneID, before string) (string, *agentv1.CommandResult) {
	deadline := time.Now().Add(observeSettleTimeout)
	var prev string
	first := true
	for {
		cur, res := e.readScreen(ctx, paneID)
		if res != nil {
			return "", res
		}
		if cur != before && !first && cur == prev {
			return cur, nil
		}
		prev, first = cur, false
		if time.Now().After(deadline) {
			return "", refuseRetryable(typev1.ReasonCode_REASON_CODE_READY_TIMEOUT,
				"the harness screen never settled after the command")
		}
		select {
		case <-ctx.Done():
			return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
				"the settle wait ended with the command context")
		case <-time.After(observeSettlePoll):
		}
	}
}

// splitLiteral cuts text into pieces of at most max bytes on UTF-8
// boundaries, so a command plus full 4 KiB args types as several bounded
// literals under the same pane lock followed by Enter. The typing cap counts
// bytes; a split never cuts a multi-byte rune, and an invalid rune boundary
// falls back to one byte so progress is always made.
func splitLiteral(text string, max int) []string {
	if max <= 0 || len(text) <= max {
		return []string{text}
	}
	var out []string
	for len(text) > 0 {
		if len(text) <= max {
			out = append(out, text)
			break
		}
		cut := max
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		if cut == 0 {
			cut = max
			for cut < len(text) && !utf8.RuneStart(text[cut]) {
				cut++
			}
			if cut > len(text) {
				cut = len(text)
			}
		}
		out = append(out, text[:cut])
		text = text[cut:]
	}
	return out
}

// cleanScreen renders one styled capture as plain text for the OBSERVE
// result: escape sequences cut, control characters dropped (untrusted
// screen text leaves the machine only as data).
func cleanScreen(styled string) string {
	return harness.StripControls(stripCSI(styled))
}

// stripCSI cuts CSI escape sequences (ESC [ ... final byte) out of s. A
// lone ESC is kept: cleanScreen's control strip drops it later.
func stripCSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
