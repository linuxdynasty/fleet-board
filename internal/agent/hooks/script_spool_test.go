package hooks_test

import (
	"os"
	"testing"
)

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
