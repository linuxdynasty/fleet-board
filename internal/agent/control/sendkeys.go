package control

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/probe"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"
)

// Sender types lines and reads screens through the observer client of
// internal/agent/tmux (doc.go). *tmux.Client satisfies it; the wiring row
// passes the server's client, tests a stub. Capture returns the visible
// screen without styling (capture-pane -p), the same read the probe
// classifies.
type Sender interface {
	Send(ctx context.Context, l typing.Line) error
	Capture(ctx context.Context, pane tmux.PaneID) (tmux.Capture, error)
}

// Compile-time proof the production client satisfies the seam.
var _ Sender = (*tmux.Client)(nil)

// CheckInputFunc reports when pane (a "%N" id) last saw a key, read at
// that moment: Observer.CheckInput with the server closed over (02h E13).
// The zero time saw no key.
type CheckInputFunc func(ctx context.Context, pane string) (time.Time, error)

// inputHoldoff is how long after a key the pane stays untypeable: the same
// fact the dashboard banner shows (Pane.last_input_time within 10 minutes,
// ui-contract-map) and the hub's human_at_keyboard window (02h). The hub
// never holds looser than this, so neither does the agent.
const inputHoldoff = 10 * time.Minute

// paneIDRE is the "%<digits>" shape typing builds lines for (refuse.go),
// re-checked here so a malformed id fails before any tmux call.
var paneIDRE = regexp.MustCompile(`^%[0-9]{1,10}$`)

// harnessCommands maps a pane's running command to the harness whose
// screen the gate classifies. It mirrors probe's harnessBinaries (probe.go):
// the four harnesses with a usage probe. Anything else is a shell or an
// unknown program, and this slice types into neither (README).
var harnessCommands = map[string]string{
	"claude":       "claude",
	"codex":        "codex",
	"muse":         "muse",
	"cursor-agent": "cursor",
}

// refuseRetryable builds a refusal the hub may send again: the pane may be
// ready next time (command_tmux.proto retryable).
func refuseRetryable(why typev1.ReasonCode, msg string) *agentv1.CommandResult {
	r := refuse(typev1.Status_STATUS_FAILED, why, msg)
	r.Retryable = true
	return r
}

// SendKeys types an OPERATOR_TEXT command: free text from an operator. The
// other purposes land in later slices of row 18. Class is NotRepeatSafe: a
// retry after UNKNOWN_OUTCOME is refused by the executor, never retyped.
func (e *Ensurer) SendKeys(ctx context.Context, cmd *agentv1.SendKeys) *agentv1.CommandResult {
	switch cmd.GetPurpose() {
	case typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_OPERATOR_TEXT:
	case typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_UNSPECIFIED:
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"every SendKeys states its purpose")
	default:
		return refuse(typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
			"purpose "+cmd.GetPurpose().String()+" lands in a later slice of row 18")
	}
	if cmd.GetAnsweringPrompt() != nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"answering_prompt is for prompt answers only")
	}
	keys := cmd.GetKeys()
	if len(keys) == 0 {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"there are no keys to type")
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
	// The keys' own shape is the caller's malformation, so it fails
	// before any tmux call; the built lines wait for the gates below.
	lines, res := e.buildLines(pane, keys)
	if res != nil {
		return res
	}
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
	// list-clients at this moment, never stored push state.
	at, err := e.checkInput(ctx, paneID)
	if err != nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the developer-input check failed")
	}
	if !at.IsZero() && e.now().Sub(at) < inputHoldoff {
		return refuseRetryable(typev1.ReasonCode_REASON_CODE_PANE_BUSY,
			"a person typed in this pane recently; nothing typed")
	}
	harness, ok := harnessCommands[command]
	if !ok {
		return refuseRetryable(typev1.ReasonCode_REASON_CODE_PANE_BUSY,
			"this slice types into harness panes only, never shells")
	}
	screen, res := e.readScreen(ctx, paneID)
	if res != nil {
		return res
	}
	if res := classifyForSend(harness, screen); res != nil {
		return res
	}
	return e.sendLines(ctx, paneID, lines)
}

// sendLines types every built line in order through the observer client and
// counts each one tmux took. A line tmux refused ends the command FAILED:
// the lines before it already landed, so the message counts what typed.
func (e *Ensurer) sendLines(ctx context.Context, paneID string, lines []typing.Line) *agentv1.CommandResult {
	for i, l := range lines {
		if err := e.sender.Send(ctx, l); err != nil {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
				fmt.Sprintf("typed %d of %d before tmux refused the rest", i, len(lines)))
		}
		e.rec.Sent(ctx, l)
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Message: fmt.Sprintf("typed %d keys into %s", len(lines), paneID),
	}
}

// paneInfo reads one pane's dead flag and running command from list-panes.
// A display-message aimed at a missing pane answers success with every
// field expanded empty (measured tmux 3.8), so absence is read from the
// pane list instead: no row for the id reads PANE_NOT_FOUND. The command
// is the pane's own text and never logged.
func (e *Ensurer) paneInfo(ctx context.Context, paneID string) (bool, string, *agentv1.CommandResult) {
	out, err := e.tmuxCall(ctx, "list-panes", "-a", "-F", "#{pane_id} #{pane_dead} #{pane_current_command}")
	if err != nil {
		return false, "", e.down(err)
	}
	for line := range strings.Lines(strings.TrimSpace(out)) {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if f[0] != paneID {
			continue
		}
		if len(f) < 2 {
			return false, "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
				"the pane row came back short")
		}
		command := ""
		if len(f) > 2 {
			command = strings.Join(f[2:], " ")
		}
		return f[1] == "1", command, nil
	}
	return false, "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_PANE_NOT_FOUND,
		"no pane "+paneID+" is on this server")
}

// readScreen returns the pane's visible screen for the gate. A capture the
// reply bound cut short is incomplete and must not be trusted as whole
// (message.go), so it reads READY_TIMEOUT, retryable, like an unsettled
// probe screen.
func (e *Ensurer) readScreen(ctx context.Context, paneID string) (string, *agentv1.CommandResult) {
	n, err := strconv.Atoi(strings.TrimPrefix(paneID, "%"))
	if err != nil || n < 0 {
		return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a pane id looks like %3")
	}
	got, err := e.sender.Capture(ctx, tmux.PaneID(n))
	if err != nil {
		return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the screen read failed")
	}
	if got.Truncated {
		return "", refuseRetryable(typev1.ReasonCode_REASON_CODE_READY_TIMEOUT,
			"the screen read was cut short")
	}
	return strings.Join(got.Lines, "\n"), nil
}

// classifyForSend gates one captured screen through the probe's pure
// screen classifier (probe.Classify: trust, then startup, then ready, so a
// prompt screen never reads as idle). It returns nil when the screen proves
// a ready prompt with an empty input line, else the refusal, typed nothing.
// Capture-pane reads carry no styling, so Cursor panes (whose empty check
// needs it, 02h) always land on READY_TIMEOUT here; row 30 keeps refusing
// them with PANE_BUSY in the probe the same way.
func classifyForSend(harness, screen string) *agentv1.CommandResult {
	switch c := probe.Classify(harness, screen); {
	case c.Outcome == probe.OutcomeReady && c.Input == probe.InputEmpty:
		return nil
	case c.Outcome == probe.OutcomeTrust:
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED,
			"a folder-trust screen shows; the owner answers it by hand")
	case c.Outcome == probe.OutcomeStartup:
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN,
			"a startup screen shows; nothing typed")
	case c.Input == probe.InputDraft:
		return refuseRetryable(typev1.ReasonCode_REASON_CODE_PANE_BUSY,
			"the input line holds typed text; nothing typed")
	case harness == "cursor":
		// Row 30: until the styled-input rule is proven, every SendKeys
		// on a Cursor pane answers PANE_BUSY. A plain capture can never
		// carry the faint attribute the empty check needs.
		return refuseRetryable(typev1.ReasonCode_REASON_CODE_PANE_BUSY,
			"a Cursor pane never proves its input line empty in this slice")
	default:
		return refuseRetryable(typev1.ReasonCode_REASON_CODE_READY_TIMEOUT,
			"the harness never reached its ready prompt")
	}
}

// buildLines turns the command's keys into send-keys lines, one per input:
// literals go with -l so tmux reads no key name in them, named keys come
// from the closed set. A constructor refusal is the caller's malformed
// input (INVALID_ARGUMENT); its reason names the shape, never the text.
func (e *Ensurer) buildLines(pane *typev1.PaneKey, keys []*typev1.KeyInput) ([]typing.Line, *agentv1.CommandResult) {
	lines := make([]typing.Line, 0, len(keys))
	for i, k := range keys {
		var l typing.Line
		var err error
		switch in := k.GetInput().(type) {
		case *typev1.KeyInput_Literal:
			l, err = typing.NewLiteral(e.rec, pane, typing.PurposeOperatorText, in.Literal)
		case *typev1.KeyInput_Key:
			tk, ok := namedKey(in.Key)
			if !ok {
				return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
					fmt.Sprintf("key %d is outside the closed named set", i))
			}
			l, err = typing.NewKeys(e.rec, pane, typing.PurposeOperatorText, tk)
		default:
			return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				fmt.Sprintf("key %d holds neither text nor a named key", i))
		}
		if err != nil {
			return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				fmt.Sprintf("key %d is not typeable: %s", i, err.Error()))
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// namedKey maps the contract's closed named set to the typing one. The
// numbers match today; the switch keeps that a compiler-checked fact
// instead of a cast across the contract boundary.
func namedKey(k typev1.NamedKey) (typing.Key, bool) {
	switch k {
	case typev1.NamedKey_NAMED_KEY_ENTER:
		return typing.KeyEnter, true
	case typev1.NamedKey_NAMED_KEY_ESCAPE:
		return typing.KeyEscape, true
	case typev1.NamedKey_NAMED_KEY_TAB:
		return typing.KeyTab, true
	case typev1.NamedKey_NAMED_KEY_BACKSPACE:
		return typing.KeyBackspace, true
	case typev1.NamedKey_NAMED_KEY_UP:
		return typing.KeyUp, true
	case typev1.NamedKey_NAMED_KEY_DOWN:
		return typing.KeyDown, true
	case typev1.NamedKey_NAMED_KEY_LEFT:
		return typing.KeyLeft, true
	case typev1.NamedKey_NAMED_KEY_RIGHT:
		return typing.KeyRight, true
	case typev1.NamedKey_NAMED_KEY_CTRL_C:
		return typing.KeyCtrlC, true
	default:
		return typing.KeyUnspecified, false
	}
}
