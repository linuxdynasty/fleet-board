package hooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// scriptTimeout bounds one synthetic hook run; the one-shot helper's own
// time cap fires first, so this only stops a wedged helper.
const scriptTimeout = 30 * time.Second

// Runner runs one hook script. *proc.Runner satisfies it; the seam keeps
// the production path on the one-shot helper (01 principle 8) while
// tests inject fakes.
type Runner interface {
	Run(ctx context.Context, sp proc.Spec) (proc.Result, error)
}

// hookHash is sha256 over the installed script bytes plus the rendered
// fleet entries: a changed hook definition prompts Codex and Muse to
// ask for approval again (01d).
func hookHash(dataDir, harness string) string {
	h := sha256.New()
	h.Write(hookScript)
	scriptPath := ScriptPath(dataDir, harness)
	for _, event := range Events(harness) {
		h.Write(freshGroup(harness, event, scriptPath))
	}
	if harness == HarnessMuse {
		if manifest, err := RenderFresh("muse-manifest", scriptPath); err == nil {
			h.Write(manifest)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// verifyHarness runs each installed script with a synthetic payload and
// no registry entry (must print nothing, exit 0), then with a throwaway
// entry (must emit the expected event). Muse cases go through the
// documented hook-test shape with a cleared environment; Cursor runs its
// beforeSubmitPrompt and stop pair (01d, 01d3).
//
// The harness feeds the payload on stdin; the one-shot helper starts
// children with empty stdin by design, so verify writes the payload to a
// file and passes --input-file. The stdin path itself is covered by the
// script tests, which run the script the way the harness does.
func verifyHarness(r Runner, dataDir, harness string) error {
	tmp, err := os.MkdirTemp("", "fleet-hooks-verify")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(work, 0o750); err != nil {
		return err
	}
	env, regPath, spoolPath := verifyEnv(tmp, harness)
	writeRegistryFile(regPath, `{"version":1,"entries":[]}`)
	mapping, stop := syntheticPayloads(harness, work, "verify-sess")
	if harness == HarnessMuse {
		mapping = hookTestShape("UserPromptSubmit", mapping)
		stop = hookTestShape("Stop", stop)
	}
	mappingFile, err := writePayloadFile(tmp, "mapping.json", mapping)
	if err != nil {
		return err
	}
	stopFile, err := writePayloadFile(tmp, "stop.json", stop)
	if err != nil {
		return err
	}
	script := ScriptPath(dataDir, harness)
	// No entry: silent, exit 0.
	res, err := runHook(r, script, harness, mappingFile, env)
	if err != nil {
		return fmt.Errorf("silent case: %w", err)
	}
	if res.stdout != "" || len(res.spool) != 0 {
		return fmt.Errorf("silent case: stdout %q spool %d lines, want silence", res.stdout, len(res.spool))
	}
	// Throwaway entry, session unknown: the mapping event learns it.
	writeRegistryFile(regPath, `{"version":1,"entries":[`+verifyEntry(harness, work, "")+`]}`)
	res, err = runHook(r, script, harness, mappingFile, env)
	if err != nil {
		return fmt.Errorf("mapping case: %w", err)
	}
	if res.stdout != "" {
		return fmt.Errorf("mapping case: stdout %q, want empty", res.stdout)
	}
	if len(res.spool) != 1 || res.spool[0]["kind"] != "session_mapping" {
		return fmt.Errorf("mapping case: spool %v, want one session_mapping event", res.spool)
	}
	// Throwaway entry with the session: the stop ends the turn.
	writeRegistryFile(regPath, `{"version":1,"entries":[`+verifyEntry(harness, work, "verify-sess")+`]}`)
	writeRegistryFile(spoolPath, "")
	res, err = runHook(r, script, harness, stopFile, env)
	if err != nil {
		return fmt.Errorf("stop case: %w", err)
	}
	if res.stdout != "" {
		return fmt.Errorf("stop case: stdout %q, want empty (no goal file)", res.stdout)
	}
	if len(res.spool) != 1 || res.spool[0]["kind"] != "turn_end" {
		return fmt.Errorf("stop case: spool %v, want one turn_end event", res.spool)
	}
	return nil
}

// verifyEnv builds the hermetic script environment: a temp HOME that is
// never the developer's, explicit throwaway registry and spool paths,
// and the pane only where the harness passes pane variables. Muse gets a
// cleared environment like hook test (only HOME), with the registry and
// spool at their HOME-based defaults.
func verifyEnv(tmp, harness string) (env []string, regPath, spoolPath string) {
	dataHome := filepath.Join(tmp, ".local", "share", "fleet-agent")
	if harness == HarnessMuse {
		regPath = filepath.Join(dataHome, "goal-registry.json")
		spoolPath = filepath.Join(dataHome, "hook-events.jsonl")
		return []string{"HOME=" + tmp}, regPath, spoolPath
	}
	regPath = filepath.Join(tmp, "registry.json")
	spoolPath = filepath.Join(tmp, "spool.jsonl")
	return []string{
		"HOME=" + tmp,
		"FLEET_GOAL_REGISTRY=" + regPath,
		"FLEET_HOOK_SPOOL=" + spoolPath,
		"TMUX_PANE=%9",
	}, regPath, spoolPath
}

func verifyEntry(harness, work, sid string) string {
	return `{"goal_id":"verify-g1","harness":` + quoteJSON(harness) + `,"pane":"%9"` +
		`,"worktree_path":` + quoteJSON(work) + `,"harness_session_id":` + quoteJSON(sid) + `}`
}

// syntheticPayloads builds the mapping and turn-end payloads from the
// probed shapes (testdata/payloads).
func syntheticPayloads(harness, work, sid string) (mapping, stop string) {
	var mapEvent, stopEvent string
	switch harness {
	case HarnessCursor:
		mapEvent, stopEvent = "beforeSubmitPrompt", "stop"
	default:
		mapEvent, stopEvent = "UserPromptSubmit", "Stop"
	}
	m := map[string]any{
		"hook_event_name": mapEvent,
		"session_id":      sid,
		"cwd":             work,
		"prompt":          "hello",
	}
	s := map[string]any{
		"hook_event_name": stopEvent,
		"session_id":      sid,
		"cwd":             work,
	}
	if harness == HarnessCursor {
		m["conversation_id"] = sid
		m["workspace_roots"] = []string{work}
		s["conversation_id"] = sid
		s["workspace_roots"] = []string{work}
		s["status"] = "completed"
		s["loop_count"] = 0
	}
	mb, merr := json.Marshal(m)
	sb, serr := json.Marshal(s)
	if merr != nil || serr != nil {
		panic(fmt.Sprintf("hooks: marshal synthetic payload: %v %v", merr, serr))
	}
	return string(mb), string(sb)
}

// hookTestShape wraps a payload the way muse plugins hook test does:
// {"event": ..., "stdin": {...}}.
func hookTestShape(event, payload string) string {
	return `{"event":` + quoteJSON(event) + `,"stdin":` + payload + `}`
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s) //nolint:errchkjson // a string cannot fail
	return string(b)
}

type hookRun struct {
	stdout string
	spool  []map[string]any
}

func runHook(r Runner, script, harness, payloadFile string, env []string) (hookRun, error) {
	var res hookRun
	python, err := proc.LookPath("python3")
	if err != nil {
		return res, fmt.Errorf("python3 not found: %w", err)
	}
	caller, err := proc.For("hooks")
	if err != nil {
		return res, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
	defer cancel()
	pres, err := r.Run(ctx, proc.Spec{
		Caller: caller,
		Path:   python,
		Args:   []string{script, harness, "--input-file", payloadFile},
		Env:    env,
	})
	if err != nil {
		// Terse on purpose: the output can echo the payload, which
		// is the developer's own prompt text.
		return res, fmt.Errorf("hook run: %w", err)
	}
	res.stdout = string(pres.Stdout)
	spool := spoolFromEnv(env)
	if spool == "" {
		return res, nil
	}
	raw, rerr := os.ReadFile(spool) //nolint:gosec // G304: the verify spool file this run created
	if rerr != nil {
		return res, nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if jerr := json.Unmarshal([]byte(line), &m); jerr != nil {
			return res, fmt.Errorf("spool line is not JSON: %w", jerr)
		}
		res.spool = append(res.spool, m)
	}
	return res, nil
}

func spoolFromEnv(env []string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "FLEET_HOOK_SPOOL="); ok {
			return v
		}
	}
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			return filepath.Join(v, ".local", "share", "fleet-agent", "hook-events.jsonl")
		}
	}
	return ""
}

func writePayloadFile(dir, name, payload string) (string, error) {
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func writeRegistryFile(path, content string) {
	_ = os.MkdirAll(filepath.Dir(path), 0o750)
	_ = os.WriteFile(path, []byte(content), 0o600)
}
