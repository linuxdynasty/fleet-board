package hooks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/hooks"
)

// Seams under test: RepoShipsOwn (a project Stop hook or a goal router
// on UserPromptSubmit means the repo owns its goals, 01d step 1).

// stageWorkdir copies one detect fixture to its real project path inside
// a fresh workdir.
func stageWorkdir(t *testing.T, fixture, dest string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "detect", fixture)) //nolint:gosec // G304: a fixture under the repo's own testdata, named by the test
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, dest)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil { //nolint:gosec // G703: the staged copy of a repo fixture
		t.Fatalf("write staged file: %v", err)
	}
	return dir
}

func TestRepoShipsOwn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		fixture string
		dest    string
		own     bool
		detail  string
	}{
		{"project stop hook", "stop-only/.claude-settings.json", ".claude/settings.json", true, "Stop"},
		{"goal router before project stop", "eshu/.claude-settings.json", ".claude/settings.json", true, "UserPromptSubmit"},
		{"cursor goal router", "cursor-owned/.cursor-hooks.json", ".cursor/hooks.json", true, "beforeSubmitPrompt"},
		{"unrelated hooks only", "clean/.claude-settings.json", ".claude/settings.json", false, ""},
		{"corrupt project file stays inert", "corrupt/.claude-settings.json", ".claude/settings.json", true, "unparseable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			own, detail := hooks.RepoShipsOwn(stageWorkdir(t, tc.fixture, tc.dest))
			if own != tc.own {
				t.Fatalf("RepoShipsOwn = %v, want %v (detail %q)", own, tc.own, detail)
			}
			if tc.detail != "" && !strings.Contains(detail, tc.detail) {
				t.Fatalf("detail %q does not name %q", detail, tc.detail)
			}
		})
	}
}

func TestRepoShipsOwnGoalRouter(t *testing.T) {
	t.Parallel()
	// A UserPromptSubmit entry that routes goals is repo-owned even
	// without a project Stop hook.
	dir := stageWorkdir(t, "clean/.claude-settings.json", ".codex/hooks.json")
	own, detail := hooks.RepoShipsOwn(dir)
	if own {
		t.Fatalf("clean codex hooks read as repo-owned: %q", detail)
	}
	dir = t.TempDir()
	router := `{"hooks": {"UserPromptSubmit": [{"hooks": [{"type": "command", "command": "python3 \"$r/scripts/goal-role-router.py\" codex"}]}]}}`
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".codex", "hooks.json"), []byte(router), 0o600); err != nil {
		t.Fatal(err)
	}
	own, detail = hooks.RepoShipsOwn(dir)
	if !own {
		t.Fatal("goal router on UserPromptSubmit not detected as repo-owned")
	}
	if !strings.Contains(detail, "UserPromptSubmit") {
		t.Fatalf("detail %q does not name the router event", detail)
	}
}

func TestRepoShipsOwnEmptyDir(t *testing.T) {
	t.Parallel()
	own, detail := hooks.RepoShipsOwn(t.TempDir())
	if own {
		t.Fatalf("empty workdir reads repo-owned: %q", detail)
	}
}
