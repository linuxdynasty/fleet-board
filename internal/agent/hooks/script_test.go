package hooks_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Seams under test: the hook script (hook.py) behind runScript — session
// mapping (E9), turn-end events (E9/E8), the Stop guard and the inert
// cases. Every case runs the real script with a throwaway registry and
// spool file and a temp HOME, never the developer's own files (G1).

type scriptOut struct {
	exit     int
	stdout   string
	spool    []map[string]any
	registry map[string]any
}

func python3(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required by the hooks and the tests: install python3 (3.9+)")
	}
	return p
}

func payload(t *testing.T, name, work, session string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "payloads", name)) //nolint:gosec // G304: a payload fixture under the repo's own testdata
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	s := string(raw)
	s = strings.ReplaceAll(s, "@WORK@", work)
	s = strings.ReplaceAll(s, "@SESSION@", session)
	return s
}

func writeRegistry(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

func runScript(t *testing.T, harness, stdin string, env map[string]string) scriptOut {
	t.Helper()
	home := t.TempDir()
	base := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	for k, v := range env {
		base = append(base, k+"="+v)
	}
	cmd := exec.CommandContext(t.Context(), python3(t), filepath.Join(".", "hook.py"), harness) //nolint:gosec // G204: tests run the hook script with fixture payloads
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = base
	out, err := cmd.Output()
	res := scriptOut{stdout: string(out)}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.exit = ee.ExitCode()
		} else {
			t.Fatalf("run script: %v", err)
		}
	}
	if spool, ok := env["FLEET_HOOK_SPOOL"]; ok {
		if raw, rerr := os.ReadFile(spool); rerr == nil { //nolint:gosec // G304: the spool file this run wrote
			for _, line := range strings.Split(string(raw), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var m map[string]any
				if jerr := json.Unmarshal([]byte(line), &m); jerr != nil {
					t.Fatalf("spool line is not JSON: %v", jerr)
				}
				res.spool = append(res.spool, m)
			}
		}
	}
	if reg, ok := env["FLEET_GOAL_REGISTRY"]; ok {
		if raw, rerr := os.ReadFile(reg); rerr == nil { //nolint:gosec // G304: the registry this run wrote
			var m map[string]any
			if jerr := json.Unmarshal(raw, &m); jerr == nil {
				res.registry = m
			}
		}
	}
	return res
}

func scriptEnv(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	env := map[string]string{
		"FLEET_GOAL_REGISTRY": filepath.Join(dir, "registry.json"),
		"FLEET_HOOK_SPOOL":    filepath.Join(dir, "spool.jsonl"),
		"TMUX_PANE":           "%5",
	}
	for k, v := range extra {
		env[k] = v
	}
	writeRegistry(t, env["FLEET_GOAL_REGISTRY"], `{"version":1,"entries":[]}`)
	return env
}

// wrapPayload wraps a payload the way muse plugins hook test does.
func wrapPayload(t *testing.T, event, stdin string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"event": event, "stdin": json.RawMessage(stdin)})
	if err != nil {
		t.Fatalf("marshal wrapped payload: %v", err)
	}
	return string(raw)
}

func entry(harness, pane, work, sid string) string {
	q := func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			panic("hooks test: marshal string: " + err.Error())
		}
		return string(b)
	}
	return `{"goal_id":"g1","harness":` + q(harness) + `,"pane":` + q(pane) +
		`,"worktree_path":` + q(work) + `,"harness_session_id":` + q(sid) + `}`
}

func registryWith(entries ...string) string {
	return `{"version":1,"entries":[` + strings.Join(entries, ",") + `]}`
}

// TestScriptSilentWithoutEntry: with no registry entry the script prints
// nothing and exits 0 for every wired event (G1).
func TestScriptSilentWithoutEntry(t *testing.T) {
	t.Parallel()
	cases := []struct {
		harness string
		event   string
		payload string
	}{
		{"claude", "UserPromptSubmit", "claude-userpromptsubmit.json"},
		{"claude", "Stop", "claude-stop.json"},
		{"codex", "PreToolUse", "codex-pretooluse.json"},
		{"codex", "Stop", "codex-stop.json"},
		{"muse", "UserPromptSubmit", "muse-userpromptsubmit.json"},
		{"muse", "Stop", "muse-stop.json"},
		{"cursor", "beforeSubmitPrompt", "cursor-beforesubmitprompt.json"},
		{"cursor", "stop", "cursor-stop.json"},
	}
	for _, tc := range cases {
		t.Run(tc.harness+"/"+tc.event, func(t *testing.T) {
			t.Parallel()
			work := t.TempDir()
			env := scriptEnv(t, nil)
			stdin := payload(t, tc.payload, work, "sess-9")
			if tc.harness == "muse" {
				// Muse hooks run with a cleared environment through the
				// documented hook-test shape, without TMUX_PANE.
				wrapped := wrapPayload(t, tc.event, stdin)
				stdin = wrapped
				delete(env, "TMUX_PANE")
			}
			res := runScript(t, tc.harness, stdin, env)
			if res.exit != 0 {
				t.Fatalf("exit = %d, want 0", res.exit)
			}
			if res.stdout != "" {
				t.Fatalf("stdout = %q, want empty", res.stdout)
			}
			if len(res.spool) != 0 {
				t.Fatalf("spool = %v, want no events", res.spool)
			}
		})
	}
}

// TestScriptMappingLearnsSession: the first mapping event after
// registration records the harness session id and spools it (E9).
func TestScriptMappingLearnsSession(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	cases := []struct {
		harness string
		payload string
		pane    string
	}{
		{"claude", "claude-userpromptsubmit.json", "%5"},
		{"codex", "codex-pretooluse.json", "%5"},
		{"cursor", "cursor-beforesubmitprompt.json", "%5"},
		{"muse", "muse-userpromptsubmit.json", ""},
	}
	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			t.Parallel()
			env := scriptEnv(t, nil)
			writeRegistry(t, env["FLEET_GOAL_REGISTRY"], registryWith(entry(tc.harness, tc.pane, work, "")))
			stdin := payload(t, tc.payload, work, "sess-9")
			if tc.harness == "muse" {
				wrapped := wrapPayload(t, "UserPromptSubmit", stdin)
				stdin = wrapped
				delete(env, "TMUX_PANE")
			}
			res := runScript(t, tc.harness, stdin, env)
			if res.exit != 0 || res.stdout != "" {
				t.Fatalf("exit = %d stdout = %q, want 0 and empty", res.exit, res.stdout)
			}
			if len(res.spool) != 1 || res.spool[0]["kind"] != "session_mapping" {
				t.Fatalf("spool = %v, want one session_mapping event", res.spool)
			}
			if res.spool[0]["goal_id"] != "g1" || res.spool[0]["harness_session_id"] != "sess-9" {
				t.Fatalf("spool = %v, want goal g1 and session sess-9", res.spool)
			}
			entries, ok := res.registry["entries"].([]any)
			if !ok || len(entries) != 1 {
				t.Fatalf("registry entries = %v, want one", res.registry["entries"])
			}
			first, ok := entries[0].(map[string]any)
			if !ok {
				t.Fatalf("registry entry = %v, want an object", entries[0])
			}
			if got := first["harness_session_id"]; got != "sess-9" {
				t.Fatalf("registry session = %v, want sess-9", got)
			}
		})
	}
}

// TestScriptSpoolFileIs0600: the hook creates hook-events.jsonl as a
// plain append 0600 file (01d2 section 8: everything the agent creates
// is 0700 folders and 0600 files).
func TestScriptSpoolFileIs0600(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	env := scriptEnv(t, nil)
	writeRegistry(t, env["FLEET_GOAL_REGISTRY"], registryWith(entry("claude", "%5", work, "")))
	stdin := payload(t, "claude-userpromptsubmit.json", work, "sess-9")
	res := runScript(t, "claude", stdin, env)
	if res.exit != 0 || len(res.spool) != 1 {
		t.Fatalf("exit = %d spool = %v, want 0 and one event", res.exit, res.spool)
	}
	fi, err := os.Stat(env["FLEET_HOOK_SPOOL"])
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("spool mode = %o; want 600", fi.Mode().Perm())
	}
}

// TestScriptMappingAmbiguousStaysSilent: with two Muse panes on one
// checkout the hook never guesses (01d).
func TestScriptMappingAmbiguousStaysSilent(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	env := scriptEnv(t, nil)
	delete(env, "TMUX_PANE")
	writeRegistry(t, env["FLEET_GOAL_REGISTRY"], registryWith(
		`{"goal_id":"g1","harness":"muse","pane":"%1","worktree_path":"`+work+`"}`,
		`{"goal_id":"g2","harness":"muse","pane":"%2","worktree_path":"`+work+`"}`,
	))
	stdin := payload(t, "muse-userpromptsubmit.json", work, "sess-9")
	wrapped := wrapPayload(t, "UserPromptSubmit", stdin)
	res := runScript(t, "muse", wrapped, env)
	if res.exit != 0 || res.stdout != "" || len(res.spool) != 0 {
		t.Fatalf("exit = %d stdout = %q spool = %v, want silence", res.exit, res.stdout, res.spool)
	}
}

// TestScriptTurnEnd: a Stop on a matched entry without a goal file
// spools turn_end and allows the stop (fail open).
func TestScriptTurnEnd(t *testing.T) {
	t.Parallel()
	cases := []struct {
		harness string
		payload string
	}{
		{"claude", "claude-stop.json"},
		{"codex", "codex-stop.json"},
		{"muse", "muse-stop.json"},
		{"cursor", "cursor-stop.json"},
	}
	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			t.Parallel()
			work := t.TempDir()
			env := scriptEnv(t, nil)
			writeRegistry(t, env["FLEET_GOAL_REGISTRY"], registryWith(entry(tc.harness, "%5", work, "sess-9")))
			stdin := payload(t, tc.payload, work, "sess-9")
			if tc.harness == "muse" {
				wrapped := wrapPayload(t, "Stop", stdin)
				stdin = wrapped
				delete(env, "TMUX_PANE")
			}
			res := runScript(t, tc.harness, stdin, env)
			if res.exit != 0 || res.stdout != "" {
				t.Fatalf("exit = %d stdout = %q, want 0 and empty", res.exit, res.stdout)
			}
			if len(res.spool) != 1 || res.spool[0]["kind"] != "turn_end" {
				t.Fatalf("spool = %v, want one turn_end event", res.spool)
			}
			if res.spool[0]["allowed"] != true {
				t.Fatalf("spool = %v, want allowed=true without a goal file", res.spool)
			}
		})
	}
}

func writeGoalFile(t *testing.T, work, first string) {
	t.Helper()
	dir := filepath.Join(work, ".fleet-board")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "goal-g1"), []byte(first+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func stopRun(t *testing.T, harness, fixture, work string) scriptOut {
	t.Helper()
	env := scriptEnv(t, nil)
	pane := "%5"
	if harness == "muse" {
		pane = ""
	}
	writeRegistry(t, env["FLEET_GOAL_REGISTRY"], registryWith(entry(harness, pane, work, "sess-9")))
	stdin := payload(t, fixture, work, "sess-9")
	if harness == "muse" {
		wrapped := wrapPayload(t, "Stop", stdin)
		stdin = wrapped
		delete(env, "TMUX_PANE")
	}
	return runScript(t, harness, stdin, env)
}

// TestScriptStopGuard: an unfinished goal blocks the stop on Claude, Muse
// and Cursor; Codex Stop is notify-only (block output unproven).
func TestScriptStopGuard(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		first   string
		blocked map[string]bool
	}{
		{"working blocks", "Work acme/repo#12 on branch fleet/12: fix the leak.", map[string]bool{"claude": true, "codex": false, "muse": true, "cursor": true}},
		{"done allows", "DONE", map[string]bool{"claude": false, "codex": false, "muse": false, "cursor": false}},
		{"watch live allows", "BLOCKED: waiting on CI WATCH=1", map[string]bool{"claude": false, "codex": false, "muse": false, "cursor": false}},
		{"owner action allows", "BLOCKED: needs review OWNER=ada ACTION=approve", map[string]bool{"claude": false, "codex": false, "muse": false, "cursor": false}},
		{"stuck blocks", "BLOCKED: no idea what next", map[string]bool{"claude": true, "codex": false, "muse": true, "cursor": true}},
	}
	fixtures := map[string]string{"claude": "claude-stop.json", "codex": "codex-stop.json", "muse": "muse-stop.json", "cursor": "cursor-stop.json"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for harness, wantBlock := range tc.blocked {
				t.Run(harness, func(t *testing.T) {
					t.Parallel()
					work := t.TempDir()
					writeGoalFile(t, work, tc.first)
					res := stopRun(t, harness, fixtures[harness], work)
					if res.exit != 0 {
						t.Fatalf("exit = %d, want 0", res.exit)
					}
					if wantBlock && res.stdout == "" {
						t.Fatal("working goal allowed the stop, want a block")
					}
					if !wantBlock && res.stdout != "" {
						t.Fatalf("stdout = %q, want empty", res.stdout)
					}
					if wantBlock {
						var ans map[string]any
						if err := json.Unmarshal([]byte(res.stdout), &ans); err != nil {
							t.Fatalf("block answer is not JSON: %v", err)
						}
						if harness == "cursor" {
							if _, ok := ans["followup_message"]; !ok {
								t.Fatalf("cursor block lacks followup_message: %v", ans)
							}
						} else if ans["decision"] != "block" {
							t.Fatalf("block lacks decision: %v", ans)
						}
					}
				})
			}
		})
	}
}

// TestScriptWatchPid: a live WATCH pid allows the stop, a dead one
// blocks it (01d).
func TestScriptWatchPid(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	writeGoalFile(t, work, fmt.Sprintf("BLOCKED: waiting on CI WATCH=%d", os.Getpid()))
	if res := stopRun(t, "claude", "claude-stop.json", work); res.stdout != "" {
		t.Fatalf("live WATCH pid blocked the stop: %q", res.stdout)
	}
	cmd := exec.CommandContext(t.Context(), "sleep", "0.01")
	if err := cmd.Start(); err != nil {
		t.Skip("cannot spawn sleep for the dead-pid case")
	}
	dead := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait sleep: %v", err)
	}
	work = t.TempDir()
	writeGoalFile(t, work, fmt.Sprintf("BLOCKED: waiting on CI WATCH=%d", dead))
	if res := stopRun(t, "claude", "claude-stop.json", work); res.stdout == "" {
		t.Fatal("dead WATCH pid allowed the stop, want a block")
	}
}

// TestScriptInertCases: SessionStart never acts, a corrupt registry fails
// open, and empty or broken input exits quietly.
func TestScriptInertCases(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	t.Run("sessionstart", func(t *testing.T) {
		t.Parallel()
		env := scriptEnv(t, nil)
		writeRegistry(t, env["FLEET_GOAL_REGISTRY"], registryWith(entry("claude", "%5", work, "sess-9")))
		stdin := `{"hook_event_name":"SessionStart","session_id":"sess-9","cwd":"` + work + `"}`
		res := runScript(t, "claude", stdin, env)
		if res.exit != 0 || res.stdout != "" || len(res.spool) != 0 {
			t.Fatalf("exit = %d stdout = %q spool = %v, want silence", res.exit, res.stdout, res.spool)
		}
	})
	t.Run("corrupt registry", func(t *testing.T) {
		t.Parallel()
		env := scriptEnv(t, nil)
		writeRegistry(t, env["FLEET_GOAL_REGISTRY"], "{oops")
		res := runScript(t, "claude", payload(t, "claude-stop.json", work, "sess-9"), env)
		if res.exit != 0 || res.stdout != "" || len(res.spool) != 0 {
			t.Fatalf("exit = %d stdout = %q spool = %v, want silence", res.exit, res.stdout, res.spool)
		}
	})
	for _, stdin := range []string{"", "\n", "{oops"} {
		t.Run("input "+strings.TrimSpace(stdin)+".", func(t *testing.T) {
			t.Parallel()
			env := scriptEnv(t, nil)
			res := runScript(t, "claude", stdin, env)
			if res.exit != 0 || res.stdout != "" || len(res.spool) != 0 {
				t.Fatalf("exit = %d stdout = %q spool = %v, want silence", res.exit, res.stdout, res.spool)
			}
		})
	}
}

// TestScriptInterrupt spools the pause without printing anything.
func TestScriptInterrupt(t *testing.T) {
	t.Parallel()
	for _, harness := range []string{"codex", "muse"} {
		t.Run(harness, func(t *testing.T) {
			t.Parallel()
			work := t.TempDir()
			env := scriptEnv(t, nil)
			pane := "%5"
			if harness == "muse" {
				pane = ""
				delete(env, "TMUX_PANE")
			}
			writeRegistry(t, env["FLEET_GOAL_REGISTRY"], registryWith(entry(harness, pane, work, "sess-9")))
			stdin := `{"hook_event_name":"Interrupt","session_id":"sess-9","cwd":"` + work + `"}`
			res := runScript(t, harness, stdin, env)
			if res.exit != 0 || res.stdout != "" {
				t.Fatalf("exit = %d stdout = %q, want 0 and empty", res.exit, res.stdout)
			}
			if len(res.spool) != 1 || res.spool[0]["kind"] != "interrupt" {
				t.Fatalf("spool = %v, want one interrupt event", res.spool)
			}
		})
	}
}

// TestScriptAbortedStopAllows: a Cursor stop whose status is not
// completed is never held, even with an unfinished goal file.
func TestScriptAbortedStopAllows(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	writeGoalFile(t, work, "Work acme/repo#12 on branch fleet/12: fix the leak.")
	env := scriptEnv(t, nil)
	writeRegistry(t, env["FLEET_GOAL_REGISTRY"], registryWith(entry("cursor", "%5", work, "sess-9")))
	stdin := `{"hook_event_name":"stop","session_id":"sess-9","conversation_id":"sess-9","workspace_roots":["` + work + `"],"status":"aborted","loop_count":0}`
	res := runScript(t, "cursor", stdin, env)
	if res.exit != 0 || res.stdout != "" {
		t.Fatalf("exit = %d stdout = %q, want 0 and empty", res.exit, res.stdout)
	}
	if len(res.spool) != 1 || res.spool[0]["kind"] != "turn_end" || res.spool[0]["allowed"] != true {
		t.Fatalf("spool = %v, want one allowed turn_end", res.spool)
	}
}

// TestScriptRepoOwnedStaysSilent: an entry from a repo-owned checkout is
// inert even for a working goal file (01d).
func TestScriptRepoOwnedStaysSilent(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	writeGoalFile(t, work, "Work acme/repo#12 on branch fleet/12: fix the leak.")
	env := scriptEnv(t, nil)
	writeRegistry(t, env["FLEET_GOAL_REGISTRY"], registryWith(
		`{"goal_id":"g1","harness":"claude","pane":"%5","worktree_path":"`+work+`","harness_session_id":"sess-9","repo_ships_own":true}`))
	res := runScript(t, "claude", payload(t, "claude-stop.json", work, "sess-9"), env)
	if res.exit != 0 || res.stdout != "" || len(res.spool) != 0 {
		t.Fatalf("exit = %d stdout = %q spool = %v, want silence", res.exit, res.stdout, res.spool)
	}
}
