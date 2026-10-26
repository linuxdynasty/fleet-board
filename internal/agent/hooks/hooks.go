package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Harness names with a defined hook shape (01d). Anything else is
// INVALID_ARGUMENT: the agent never guesses a config layout.
const (
	HarnessClaude = "claude"
	HarnessCodex  = "codex"
	HarnessMuse   = "muse"
	HarnessCursor = "cursor"
)

// HookVersion is the version the hook scripts carry. The settings command
// strings stay fixed across upgrades; a script change moves this version
// and the definition hash, and Codex and Muse ask the owner to approve
// the hooks again (01d).
const HookVersion = "1"

// ScriptName is the one shared hook script installed per harness directory.
const ScriptName = "fleet-goal-hook.py"

// Supported reports whether the harness has a defined hook shape.
func Supported(name string) bool {
	switch name {
	case HarnessClaude, HarnessCodex, HarnessMuse, HarnessCursor:
		return true
	}
	return false
}

// Events is the shared definition's per-harness row: which events carry a
// fleet entry. SessionStart stays inert everywhere (it fires with the
// first prompt, no earlier than the mapping event, and never refires in a
// reused pane); Codex additionally wires UserPromptSubmit and PreToolUse
// because Codex fires no prompt hook for /goal lines, so the session id
// is learned from the first prompt or tool call instead (02h).
func Events(harness string) []string {
	switch harness {
	case HarnessClaude:
		return []string{"SessionStart", "UserPromptSubmit", "Stop"}
	case HarnessCodex:
		return []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "Stop", "Interrupt"}
	case HarnessMuse:
		return []string{"SessionStart", "UserPromptSubmit", "Stop", "Interrupt"}
	case HarnessCursor:
		return []string{"beforeSubmitPrompt", "stop"}
	}
	return nil
}

// HooksDir is the harness directory under the agent data directory that
// holds the hook scripts.
func HooksDir(dataDir, harness string) string {
	return dataDir + "/hooks/" + harness
}

// ScriptPath is the installed hook script the settings entries point at.
func ScriptPath(dataDir, harness string) string {
	return HooksDir(dataDir, harness) + "/" + ScriptName
}

// Command is the fixed settings command string for a harness script. It
// names the script and the harness, fails open without python3 (a hook
// must never break a turn), and never changes across upgrades.
func Command(harness, scriptPath string) string {
	return "command -v python3 >/dev/null 2>&1 || exit 0; exec python3 \"" + scriptPath + "\" " + harness
}

// IsFleetCommand is the merge match rule: an entry is the agent's own
// exactly when its command points into the fleet hooks directory.
func IsFleetCommand(cmd, hooksDir string) bool {
	if cmd == "" || hooksDir == "" {
		return false
	}
	return strings.Contains(cmd, hooksDir+"/") || strings.HasSuffix(cmd, hooksDir)
}

// Trust maps a harness to its hook trust after install. The agent never
// approves anything: PENDING names what the person types (01d).
func Trust(harness string) (typev1.HookTrustState, string) {
	switch harness {
	case HarnessCodex:
		return typev1.HookTrustState_HOOK_TRUST_STATE_PENDING,
			"Open /hooks in Codex and approve the fleet hooks"
	case HarnessMuse:
		return typev1.HookTrustState_HOOK_TRUST_STATE_PENDING,
			"muse plugins approve fleet-goal-hooks"
	default:
		// Claude Code needs no approval for user hooks; Cursor has no
		// hook approval step (01d3), so neither ever reports PENDING.
		return typev1.HookTrustState_HOOK_TRUST_STATE_NOT_REQUIRED, ""
	}
}

// RenderFresh renders a fresh user-level config (or, for harness
// "muse-manifest", the plugin manifest) pointing at scriptPath. It is the
// base for a missing user file and the golden for the merge.
func RenderFresh(harness, scriptPath string) ([]byte, error) {
	var v any
	switch harness {
	case HarnessClaude:
		v = freshClaude(scriptPath)
	case HarnessCodex:
		v = freshCodex(scriptPath)
	case HarnessMuse:
		v = freshMuse(scriptPath)
	case "muse-manifest":
		v = struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}{Name: "fleet-goal-hooks", Version: HookVersion}
	case HarnessCursor:
		v = freshCursor(scriptPath)
	default:
		return nil, fmt.Errorf("hooks: unsupported harness %q", harness)
	}
	out, err := marshalJSON(v, "  ")
	if err != nil {
		return nil, fmt.Errorf("hooks: render %s config: %w", harness, err)
	}
	return out, nil
}

// marshalJSON encodes v without HTML escaping, so shell commands keep
// their literal characters; indent is "" for compact or "  " for fresh
// files. The encoder appends one newline.
func marshalJSON(v any, indent string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", indent)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type hookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type claudeGroup struct {
	Matcher string    `json:"matcher"`
	Hooks   []hookCmd `json:"hooks"`
}

type codexGroup struct {
	Hooks []hookCmd `json:"hooks"`
}

type cursorEntry struct {
	Command   string `json:"command"`
	Timeout   int    `json:"timeout,omitempty"`
	LoopLimit int    `json:"loop_limit,omitempty"`
}

func fleetCmd(harness, scriptPath string) hookCmd {
	return hookCmd{Type: "command", Command: Command(harness, scriptPath), Timeout: 10}
}

func freshClaude(scriptPath string) any {
	group := func() []claudeGroup {
		return []claudeGroup{{Matcher: "", Hooks: []hookCmd{fleetCmd(HarnessClaude, scriptPath)}}}
	}
	return struct {
		Hooks struct {
			SessionStart     []claudeGroup `json:"SessionStart"`
			UserPromptSubmit []claudeGroup `json:"UserPromptSubmit"`
			Stop             []claudeGroup `json:"Stop"`
		} `json:"hooks"`
	}{Hooks: struct {
		SessionStart     []claudeGroup `json:"SessionStart"`
		UserPromptSubmit []claudeGroup `json:"UserPromptSubmit"`
		Stop             []claudeGroup `json:"Stop"`
	}{SessionStart: group(), UserPromptSubmit: group(), Stop: group()}}
}

func freshCodex(scriptPath string) any {
	group := func() []codexGroup {
		return []codexGroup{{Hooks: []hookCmd{fleetCmd(HarnessCodex, scriptPath)}}}
	}
	return struct {
		Hooks struct {
			SessionStart     []codexGroup `json:"SessionStart"`
			UserPromptSubmit []codexGroup `json:"UserPromptSubmit"`
			PreToolUse       []codexGroup `json:"PreToolUse"`
			Stop             []codexGroup `json:"Stop"`
			Interrupt        []codexGroup `json:"Interrupt"`
		} `json:"hooks"`
	}{Hooks: struct {
		SessionStart     []codexGroup `json:"SessionStart"`
		UserPromptSubmit []codexGroup `json:"UserPromptSubmit"`
		PreToolUse       []codexGroup `json:"PreToolUse"`
		Stop             []codexGroup `json:"Stop"`
		Interrupt        []codexGroup `json:"Interrupt"`
	}{
		SessionStart:     group(),
		UserPromptSubmit: group(),
		PreToolUse:       group(),
		Stop:             group(),
		Interrupt:        group(),
	}}
}

func freshMuse(scriptPath string) any {
	entry := func() hookCmd {
		return hookCmd{Type: "command", Command: Command(HarnessMuse, scriptPath)}
	}
	group := func() []claudeGroup {
		return []claudeGroup{{Matcher: "", Hooks: []hookCmd{entry()}}}
	}
	return struct {
		Hooks struct {
			SessionStart     []claudeGroup `json:"SessionStart"`
			UserPromptSubmit []claudeGroup `json:"UserPromptSubmit"`
			Stop             []claudeGroup `json:"Stop"`
			Interrupt        []claudeGroup `json:"Interrupt"`
		} `json:"hooks"`
	}{Hooks: struct {
		SessionStart     []claudeGroup `json:"SessionStart"`
		UserPromptSubmit []claudeGroup `json:"UserPromptSubmit"`
		Stop             []claudeGroup `json:"Stop"`
		Interrupt        []claudeGroup `json:"Interrupt"`
	}{
		SessionStart:     group(),
		UserPromptSubmit: group(),
		Stop:             group(),
		Interrupt:        group(),
	}}
}

func freshCursor(scriptPath string) any {
	return struct {
		Version int `json:"version"`
		Hooks   struct {
			BeforeSubmitPrompt []cursorEntry `json:"beforeSubmitPrompt"`
			Stop               []cursorEntry `json:"stop"`
		} `json:"hooks"`
	}{
		Version: 1,
		Hooks: struct {
			BeforeSubmitPrompt []cursorEntry `json:"beforeSubmitPrompt"`
			Stop               []cursorEntry `json:"stop"`
		}{
			BeforeSubmitPrompt: []cursorEntry{{Command: Command(HarnessCursor, scriptPath), Timeout: 10}},
			Stop:               []cursorEntry{{Command: Command(HarnessCursor, scriptPath), Timeout: 10, LoopLimit: 5}},
		},
	}
}
