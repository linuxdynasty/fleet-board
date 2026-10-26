package hooks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/hooks"
)

// Seams under test: MergeUserConfig (merge-don't-replace, backup, atomic
// write) and RemoveUserConfig (uninstall removes only fleet entries).

func copyFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "merge", name)) //nolint:gosec // G304: a fixture under the repo's own testdata, named by the test
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, raw, 0o600); err != nil { //nolint:gosec // G703: the staged copy of a repo fixture
		t.Fatalf("copy fixture: %v", err)
	}
	return path
}

// TestMergeKeepsEverythingElseByteIdentical merges fleet entries into a
// user file that already holds other tools' entries, a stale fleet entry
// and odd formatting, and requires every other byte to survive (G1).
func TestMergeKeepsEverythingElseByteIdentical(t *testing.T) {
	t.Parallel()
	path := copyFixture(t, "claude-user.json")
	before, err := os.ReadFile(path) //nolint:gosec // G304: the staged copy this test wrote
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}

	changed, backup, err := hooks.MergeUserConfig(path, "claude", "/data")
	if err != nil {
		t.Fatalf("MergeUserConfig: %v", err)
	}
	if !changed {
		t.Fatal("MergeUserConfig reported no change, want a merge (stale fleet entry, missing Stop/SessionStart fleet groups)")
	}
	if backup == "" {
		t.Fatal("MergeUserConfig made no backup")
	}
	backupBytes, err := os.ReadFile(backup) //nolint:gosec // G304: the backup this merge wrote
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backupBytes) != string(before) {
		t.Fatal("backup is not byte-identical with the pre-merge file")
	}

	after, err := os.ReadFile(path) //nolint:gosec // G304: the file this merge wrote
	if err != nil {
		t.Fatalf("read merged: %v", err)
	}
	// Every non-fleet region survives verbatim, in order.
	keep := []string{
		`"mcpServers": {`,
		`"demo-server"`,
		`"PreToolUse": [{"matcher": "Edit"`,
		`goal-role-router.py\" claude-dispatch"`,
		`goal-continue.sh`,
		`"statusLine": {"type": "command","command": "demo-status"}`,
	}
	last := -1
	for _, k := range keep {
		i := strings.Index(string(after), k)
		if i < 0 {
			t.Fatalf("merged file lost non-fleet region %q:\n%s", k, after)
		}
		if i < last {
			t.Fatalf("merged file reordered non-fleet region %q", k)
		}
		last = i
	}
	// The stale fleet entry is gone, the current command is present.
	if strings.Contains(string(after), "--old-flag") {
		t.Fatalf("merged file kept the stale fleet entry:\n%s", after)
	}
	// The command holds JSON escaping in the file, so count the script path.
	if n := strings.Count(string(after), "/data/hooks/claude/fleet-goal-hook.py"); n != 3 {
		t.Fatalf("merged file holds %d current fleet commands, want 3 (SessionStart, UserPromptSubmit, Stop)", n)
	}

	// A second merge is a no-op: the file is untouched.
	infoBefore, _ := os.Stat(path)
	changed, backup2, err := hooks.MergeUserConfig(path, "claude", "/data")
	if err != nil {
		t.Fatalf("second MergeUserConfig: %v", err)
	}
	if changed || backup2 != "" {
		t.Fatal("second MergeUserConfig changed the file or backed up, want a no-op")
	}
	infoAfter, _ := os.Stat(path)
	if infoBefore.ModTime() != infoAfter.ModTime() {
		t.Fatal("second MergeUserConfig rewrote the file, want it untouched")
	}
}

func TestMergeCreatesMissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	changed, backup, err := hooks.MergeUserConfig(path, "cursor", "/data")
	if err != nil {
		t.Fatalf("MergeUserConfig: %v", err)
	}
	if !changed {
		t.Fatal("MergeUserConfig reported no change for a missing file")
	}
	if backup != "" {
		t.Fatalf("MergeUserConfig backed up a missing file: %q", backup)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the file this merge wrote
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if !strings.Contains(string(raw), "/data/hooks/cursor/fleet-goal-hook.py") ||
		!strings.Contains(string(raw), `"loop_limit": 5`) {
		t.Fatalf("created file has no fleet cursor entry:\n%s", raw)
	}
}

func TestMergeLeavesBadFileUntouched(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"unparseable.json", "wrong-shape.json"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := copyFixture(t, name)
			before, _ := os.ReadFile(path) //nolint:gosec // G304: the staged copy this test wrote
			if _, _, err := hooks.MergeUserConfig(path, "claude", "/data"); err == nil {
				t.Fatalf("MergeUserConfig(%s) succeeded, want HOOKS_INSTALL_FAILED", name)
			}
			after, _ := os.ReadFile(path) //nolint:gosec // G304: the staged copy this test wrote
			if string(after) != string(before) {
				t.Fatalf("MergeUserConfig(%s) touched the file on failure", name)
			}
		})
	}
}

func TestRemoveKeepsOthersAndDropsFleet(t *testing.T) {
	t.Parallel()
	path := copyFixture(t, "claude-user.json")
	if _, _, err := hooks.MergeUserConfig(path, "claude", "/data"); err != nil {
		t.Fatalf("MergeUserConfig: %v", err)
	}
	changed, err := hooks.RemoveUserConfig(path, "claude", "/data")
	if err != nil {
		t.Fatalf("RemoveUserConfig: %v", err)
	}
	if !changed {
		t.Fatal("RemoveUserConfig reported no change after an install")
	}
	after, _ := os.ReadFile(path) //nolint:gosec // G304: the staged copy this test wrote
	if strings.Contains(string(after), "/data/hooks/claude/") {
		t.Fatalf("removed file still names the fleet hooks dir:\n%s", after)
	}
	for _, k := range []string{`"mcpServers"`, `goal-role-router.py`, `goal-continue.sh`, `"statusLine"`} {
		if !strings.Contains(string(after), k) {
			t.Fatalf("removed file lost non-fleet region %q:\n%s", k, after)
		}
	}
	// Removing again is a no-op.
	if changed, err := hooks.RemoveUserConfig(path, "claude", "/data"); err != nil || changed {
		t.Fatalf("second RemoveUserConfig = (%v, %v), want (false, nil)", changed, err)
	}
}

// TestMergeKeepsUserCommandsInMixedGroups: a group holding the user's
// command next to a stale fleet command keeps the user's bytes; only
// the fleet entry is replaced.
func TestMergeKeepsUserCommandsInMixedGroups(t *testing.T) {
	t.Parallel()
	raw := `{"hooks":{"UserPromptSubmit":[{"matcher":"","hooks":[{"type":"command","command":"my-linter"},{"type":"command","command":"command -v python3 >/dev/null 2>&1 || exit 0; exec python3 \"/data/hooks/claude/fleet-goal-hook.py\" claude --old-flag"}]}]}}`
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, _, err := hooks.MergeUserConfig(path, "claude", "/data")
	if err != nil {
		t.Fatalf("MergeUserConfig: %v", err)
	}
	if !changed {
		t.Fatal("MergeUserConfig reported no change, want the stale fleet entry replaced")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read merged: %v", err)
	}
	if !strings.Contains(string(after), `"command":"my-linter"`) {
		t.Fatalf("merged file lost the user command:\n%s", after)
	}
	if strings.Contains(string(after), "--old-flag") {
		t.Fatalf("merged file kept the stale fleet entry:\n%s", after)
	}
	// The missing events are appended fresh, so 3 fleet commands total;
	// the mixed group itself holds exactly 1.
	if n := strings.Count(string(after), `/data/hooks/claude/fleet-goal-hook.py`); n != 3 {
		t.Fatalf("merged file holds %d current fleet commands, want 3 (one per event)", n)
	}
	group := `"my-linter"},{"type":"command","command":"command -v python3`
	if !strings.Contains(string(after), group) {
		t.Fatalf("mixed group does not hold user command then fleet command:\n%s", after)
	}
	changed, _, err = hooks.MergeUserConfig(path, "claude", "/data")
	if err != nil {
		t.Fatalf("second MergeUserConfig: %v", err)
	}
	if changed {
		t.Fatal("second MergeUserConfig changed the file, want a no-op")
	}
}

// TestRemoveKeepsUserCommandsInMixedGroups: uninstall drops only the
// fleet entries; a group left with the user's commands survives.
func TestRemoveKeepsUserCommandsInMixedGroups(t *testing.T) {
	t.Parallel()
	raw := `{"hooks":{"UserPromptSubmit":[{"matcher":"","hooks":[{"type":"command","command":"my-linter"},{"type":"command","command":"command -v python3 >/dev/null 2>&1 || exit 0; exec python3 \"/data/hooks/claude/fleet-goal-hook.py\" claude"}]}]}}`
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := hooks.RemoveUserConfig(path, "claude", "/data")
	if err != nil {
		t.Fatalf("RemoveUserConfig: %v", err)
	}
	if !changed {
		t.Fatal("RemoveUserConfig reported no change, want the fleet entry dropped")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read uninstalled: %v", err)
	}
	if !strings.Contains(string(after), `"command":"my-linter"`) {
		t.Fatalf("uninstall deleted the user command:\n%s", after)
	}
	if strings.Contains(string(after), "fleet-goal-hook.py") {
		t.Fatalf("uninstall kept a fleet entry:\n%s", after)
	}
}
