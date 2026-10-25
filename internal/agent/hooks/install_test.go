package hooks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/hooks"
)

// Seams under test: Ensure (install plus verify plus GoalHooksState) and
// Uninstall. Home and the data dir are always temp dirs; the real
// ~/.claude, ~/.codex, ~/.cursor and plugin dirs are never touched (G1).

func laneDirs(t *testing.T) (home, data, work string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	data = filepath.Join(base, "data")
	work = filepath.Join(base, "work")
	for _, d := range []string{home, data, work} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return home, data, work
}

func scriptSource(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".", "hook.py"))
	if err != nil {
		t.Fatalf("read hook.py: %v", err)
	}
	return raw
}

// TestEnsurePerHarness installs and verifies every harness against a
// clean checkout.
func TestEnsurePerHarness(t *testing.T) {
	t.Parallel()
	cases := []struct {
		harness string
		trust   typev1.HookTrustState
		config  string
	}{
		{"claude", typev1.HookTrustState_HOOK_TRUST_STATE_NOT_REQUIRED, ".claude/settings.json"},
		{"codex", typev1.HookTrustState_HOOK_TRUST_STATE_PENDING, ".codex/hooks.json"},
		{"muse", typev1.HookTrustState_HOOK_TRUST_STATE_PENDING, "hooks/muse"},
		{"cursor", typev1.HookTrustState_HOOK_TRUST_STATE_NOT_REQUIRED, ".cursor/hooks.json"},
	}
	for _, tc := range cases {
		t.Run(tc.harness, func(t *testing.T) {
			t.Parallel()
			home, data, work := laneDirs(t)
			state, err := hooks.Ensure(home, data, tc.harness, work, "")
			if err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			if !state.GetInstalled() || !state.GetVerified() {
				t.Fatalf("state = %v, want installed and verified", state)
			}
			if state.GetHookVersion() != "1" {
				t.Fatalf("hook_version = %q, want 1", state.GetHookVersion())
			}
			if len(state.GetHookHash()) != 64 {
				t.Fatalf("hook_hash = %q, want 64 hex chars", state.GetHookHash())
			}
			if state.GetTrust() != tc.trust {
				t.Fatalf("trust = %v, want %v", state.GetTrust(), tc.trust)
			}
			if tc.trust == typev1.HookTrustState_HOOK_TRUST_STATE_PENDING && state.GetTrustApproveCommand() == "" {
				t.Fatal("pending trust without an approve command")
			}
			if tc.trust != typev1.HookTrustState_HOOK_TRUST_STATE_PENDING && state.GetTrustApproveCommand() != "" {
				t.Fatalf("approve command = %q, want empty", state.GetTrustApproveCommand())
			}
			if state.GetRepoShipsOwn() {
				t.Fatal("clean checkout reads repo-owned")
			}
			if !strings.HasSuffix(state.GetConfigPath(), tc.config) {
				t.Fatalf("config_path = %q, want suffix %q", state.GetConfigPath(), tc.config)
			}
			script, err := os.ReadFile(filepath.Join(data, "hooks", tc.harness, "fleet-goal-hook.py")) //nolint:gosec // G304: the script this Ensure wrote
			if err != nil {
				t.Fatalf("read installed script: %v", err)
			}
			if string(script) != string(scriptSource(t)) {
				t.Fatal("installed script differs from hook.py")
			}
			// A second run is a no-op: same hash, no backup churn.
			again, err := hooks.Ensure(home, data, tc.harness, work, "")
			if err != nil {
				t.Fatalf("second Ensure: %v", err)
			}
			if again.GetHookHash() != state.GetHookHash() {
				t.Fatal("second Ensure changed the hash")
			}
			var backups []string
			_ = filepath.Walk(home, func(path string, info os.FileInfo, err error) error {
				if err == nil && strings.Contains(filepath.Base(path), ".fleet-backup.") {
					backups = append(backups, path)
				}
				return nil
			})
			if len(backups) != 0 {
				t.Fatalf("fresh install wrote backups: %v", backups)
			}
		})
	}
}

// TestEnsureMergesExisting keeps a lived-in user file intact.
func TestEnsureMergesExisting(t *testing.T) {
	t.Parallel()
	home, data, work := laneDirs(t)
	before := "{\"hooks\": {\"PreToolUse\": [{\"matcher\": \"Edit\"}]}, \"theme\": \"dark\"}\n"
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := hooks.Ensure(home, data, "claude", work, "")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !state.GetVerified() {
		t.Fatal("want verified")
	}
	after, _ := os.ReadFile(path) //nolint:gosec // G304: the user file this Ensure wrote
	if !strings.Contains(string(after), `"theme": "dark"`) || !strings.Contains(string(after), `"Edit"`) {
		t.Fatalf("lived-in entries lost:\n%s", after)
	}
	backups, _ := filepath.Glob(path + ".fleet-backup.*")
	if len(backups) != 1 {
		t.Fatalf("want one backup, got %v", backups)
	}
	kept, _ := os.ReadFile(backups[0])
	if string(kept) != before {
		t.Fatal("backup is not byte-identical with the pre-install file")
	}
}

// TestEnsureRepoOwned leaves the workdir alone but still reports it.
func TestEnsureRepoOwned(t *testing.T) {
	t.Parallel()
	home, data, work := laneDirs(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "detect", "eshu", ".claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(work, ".claude")
	if err := os.MkdirAll(claudeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(project, raw, 0o600); err != nil { //nolint:gosec // G703: the staged copy of a repo fixture
		t.Fatal(err)
	}
	state, err := hooks.Ensure(home, data, "claude", work, "")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !state.GetRepoShipsOwn() {
		t.Fatal("want repo_ships_own=true")
	}
	kept, _ := os.ReadFile(project) //nolint:gosec // G304: the staged copy of a repo fixture
	if string(kept) != string(raw) {
		t.Fatal("project hooks file changed")
	}
	entries, _ := os.ReadDir(work)
	if len(entries) != 1 {
		t.Fatalf("workdir gained files: %v", entries)
	}
}

// TestEnsureFailures: a bad harness is INVALID_ARGUMENT, an unparseable
// user file is HOOKS_INSTALL_FAILED with the file untouched.
func TestEnsureFailures(t *testing.T) {
	t.Parallel()
	home, data, work := laneDirs(t)
	if _, err := hooks.Ensure(home, data, "opencode", work, ""); hooks.ReasonOf(err) != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("bad harness reason = %v", err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.Ensure(home, data, "claude", work, ""); hooks.ReasonOf(err) != typev1.ReasonCode_REASON_CODE_HOOKS_INSTALL_FAILED {
		t.Fatalf("bad file reason = %v", err)
	}
	kept, _ := os.ReadFile(path) //nolint:gosec // G304: the user file this test wrote
	if string(kept) != "{oops" {
		t.Fatal("unparseable file touched on failure")
	}
}

// TestUninstall removes only fleet entries and deletes the scripts.
func TestUninstall(t *testing.T) {
	t.Parallel()
	home, data, work := laneDirs(t)
	path := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"hooks\": {}, \"other\": 1}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hooks.Ensure(home, data, "codex", work, ""); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	changed, err := hooks.Uninstall(home, data, "codex")
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !changed {
		t.Fatal("Uninstall reported no change after an install")
	}
	after, _ := os.ReadFile(path) //nolint:gosec // G304: the user file this test wrote
	if strings.Contains(string(after), "fleet-goal-hook") {
		t.Fatalf("fleet entries survive uninstall:\n%s", after)
	}
	if !strings.Contains(string(after), `"other": 1`) {
		t.Fatalf("other entries lost:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(data, "hooks", "codex", "fleet-goal-hook.py")); !os.IsNotExist(err) {
		t.Fatal("script survives uninstall")
	}
	backups, _ := filepath.Glob(path + ".fleet-backup.*")
	if len(backups) == 0 {
		t.Fatal("uninstall dropped the backup")
	}
	if changed, err := hooks.Uninstall(home, data, "codex"); err != nil || changed {
		t.Fatalf("second Uninstall = (%v, %v), want (false, nil)", changed, err)
	}
}

// TestUninstallMuse removes the plugin bundle files.
func TestUninstallMuse(t *testing.T) {
	t.Parallel()
	home, data, work := laneDirs(t)
	if _, err := hooks.Ensure(home, data, "muse", work, ""); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	bundle := filepath.Join(data, "hooks", "muse")
	for _, f := range []string{"fleet-goal-hook.py", filepath.Join("hooks", "hooks.json"), filepath.Join(".claude-plugin", "plugin.json")} {
		if _, err := os.Stat(filepath.Join(bundle, f)); err != nil {
			t.Fatalf("bundle file %s missing after install: %v", f, err)
		}
	}
	if _, err := hooks.Uninstall(home, data, "muse"); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	for _, f := range []string{"fleet-goal-hook.py", filepath.Join("hooks", "hooks.json"), filepath.Join(".claude-plugin", "plugin.json")} {
		if _, err := os.Stat(filepath.Join(bundle, f)); !os.IsNotExist(err) {
			t.Fatalf("bundle file %s survives uninstall", f)
		}
	}
}
