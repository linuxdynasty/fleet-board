package hooks

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

//go:embed hook.py
var hookScript []byte

// UserConfigPath is the user-level settings file a harness reads its
// hooks from. Muse has none: its hooks come from the plugin bundle, so
// this returns "" for muse and the bundle directory is the config path.
func UserConfigPath(home, harness string) string {
	switch harness {
	case HarnessClaude:
		return filepath.Join(home, ".claude", "settings.json")
	case HarnessCodex:
		return filepath.Join(home, ".codex", "hooks.json")
	case HarnessCursor:
		return filepath.Join(home, ".cursor", "hooks.json")
	}
	return ""
}

// Ensure installs the fleet goal hooks for one harness in a checkout:
// detect a repo-owned setup, write the scripts, merge the user config
// (or the Muse plugin bundle), and verify the scripts against synthetic
// payloads. Home and the data dir are the developer's own directories;
// the workdir is never changed. expectedVersion is what the hub expects;
// the agent always installs and reports what it has.
func Ensure(home, dataDir, harness, workdir, expectedVersion string) (*typev1.GoalHooksState, error) {
	r, err := proc.NewRunner(nil)
	if err != nil {
		return nil, installFailed("install", "", err)
	}
	return EnsureWithRunner(r, home, dataDir, harness, workdir, expectedVersion)
}

// EnsureWithRunner is Ensure with the one-shot runner injected, so the
// command path runs its kills through its own telemetry. *proc.Runner
// satisfies Runner.
func EnsureWithRunner(r Runner, home, dataDir, harness, workdir, expectedVersion string) (*typev1.GoalHooksState, error) {
	_ = expectedVersion
	if !Supported(harness) {
		return nil, invalidArg("install", "", fmt.Errorf("unsupported harness %q", harness))
	}
	shipsOwn, _ := RepoShipsOwn(workdir)
	scriptPath, err := writeScript(dataDir, harness)
	if err != nil {
		return nil, installFailed("scripts", scriptPath, err)
	}
	configPath := UserConfigPath(home, harness)
	if harness == HarnessMuse {
		configPath = HooksDir(dataDir, harness)
		if err := writeBundle(dataDir); err != nil {
			return nil, installFailed("bundle", configPath, err)
		}
	} else if _, _, err := MergeUserConfig(configPath, harness, dataDir); err != nil {
		return nil, err
	}
	if verr := verifyHarness(r, dataDir, harness); verr != nil {
		return nil, installFailed("verify", scriptPath, verr)
	}
	trust, approve := Trust(harness)
	return &typev1.GoalHooksState{
		Installed:           true,
		Verified:            true,
		HookVersion:         HookVersion,
		HookHash:            hookHash(dataDir, harness),
		RepoShipsOwn:        shipsOwn,
		Trust:               trust,
		TrustApproveCommand: approve,
		ConfigPath:          configPath,
	}, nil
}

// Uninstall removes only the fleet entries from the user config, keeps
// the backup, and deletes the scripts (and the Muse bundle files). A
// harness with nothing installed is a no-op.
func Uninstall(home, dataDir, harness string) (bool, error) {
	if !Supported(harness) {
		return false, invalidArg("uninstall", "", fmt.Errorf("unsupported harness %q", harness))
	}
	changed := false
	if harness == HarnessMuse {
		bundle := HooksDir(dataDir, harness)
		for _, rel := range []string{
			ScriptName,
			filepath.Join("hooks", "hooks.json"),
			filepath.Join(".claude-plugin", "plugin.json"),
		} {
			if err := os.Remove(filepath.Join(bundle, rel)); err == nil {
				changed = true
			} else if !os.IsNotExist(err) {
				return false, installFailed("uninstall", bundle, err)
			}
		}
		for _, dir := range []string{
			filepath.Join(bundle, "hooks"),
			filepath.Join(bundle, ".claude-plugin"),
			bundle,
		} {
			_ = os.Remove(dir) // best effort: only empty dirs go
		}
		return changed, nil
	}
	if c, err := RemoveUserConfig(UserConfigPath(home, harness), harness, dataDir); err != nil {
		return false, err
	} else if c {
		changed = true
	}
	if err := os.Remove(ScriptPath(dataDir, harness)); err == nil {
		changed = true
	} else if !os.IsNotExist(err) {
		return false, installFailed("uninstall", ScriptPath(dataDir, harness), err)
	}
	return changed, nil
}

// writeScript installs the shared hook script, executable, under the
// agent data dir.
func writeScript(dataDir, harness string) (string, error) {
	dir := HooksDir(dataDir, harness)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: the data dir holds executed hook scripts
		return "", err
	}
	path := filepath.Join(dir, ScriptName)
	if err := writeAtomic(path, hookScript, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// writeBundle refreshes the Muse plugin package: the manifest, the
// Claude-compatible hooks file and the script live together in the
// bundle directory, which the owner installs once with
// muse plugins install <bundle> --scope user.
func writeBundle(dataDir string) error {
	dir := HooksDir(dataDir, HarnessMuse)
	manifest, err := RenderFresh("muse-manifest", ScriptPath(dataDir, HarnessMuse))
	if err != nil {
		return err
	}
	hooksFile, err := RenderFresh(HarnessMuse, ScriptPath(dataDir, HarnessMuse))
	if err != nil {
		return err
	}
	for _, f := range []struct {
		rel  string
		data []byte
	}{
		{filepath.Join(".claude-plugin", "plugin.json"), manifest},
		{filepath.Join("hooks", "hooks.json"), hooksFile},
	} {
		path := filepath.Join(dir, f.rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: the data dir holds executed hook scripts
			return err
		}
		if err := writeAtomic(path, f.data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
