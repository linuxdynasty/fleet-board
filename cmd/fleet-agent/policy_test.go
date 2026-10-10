package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil { //nolint:gosec // G301: a test folder
		t.Fatal(err)
	}
	return path
}

func readPolicy(t *testing.T, dir string) policy.Result {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, policy.File)) //nolint:gosec // G304: a test folder
	if err != nil {
		t.Fatalf("no policy file: %v", err)
	}
	res := policy.Parse(data)
	if res.Problem != nil {
		t.Fatalf("policy.yaml does not parse: %v\n%s", res.Problem, data)
	}
	return res
}

func enrollArgs(t *testing.T, h *conntest.Hub, more ...string) []string {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	return append([]string{"--state-dir", state}, append(more, "enroll", "--hub", h.Addr, "--token", h.Token(t, time.Now().Add(time.Hour)))...)
}

func TestEnrollWritesTheStarterPolicyWhereTheConfigFolderIs(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	t.Run("--config-dir and --repos", func(t *testing.T) {
		t.Parallel()
		repos := mkdir(t, filepath.Join(t.TempDir(), "repos"))
		cfg := filepath.Join(t.TempDir(), "cfg")
		r := agentIn(t, t.TempDir(), nil, append(enrollArgs(t, h, "--config-dir", cfg), "--repos", repos)...)
		if r.code != 0 {
			t.Fatalf("enroll = %d: %s", r.code, r.stderr)
		}
		res := readPolicy(t, cfg)
		if got := res.Policy.AllowedRepoRoots; len(got) != 1 || got[0] != repos {
			t.Errorf("allowed_repo_roots = %v, want [%s]", got, repos)
		}
		fi, _ := os.Stat(filepath.Join(cfg, policy.File))
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("policy.yaml mode = %v, want 0644", fi.Mode().Perm())
		}
		if !strings.Contains(r.stderr, "fleet-agent: wrote "+filepath.Join(cfg, policy.File)) {
			t.Errorf("stderr %q does not say where the file went", r.stderr)
		}
		if strings.Count(r.stdout, "\n") != 1 {
			t.Errorf("stdout %q should stay the one enrolled line", r.stdout)
		}
	})
	t.Run("no --repos and no terminal", func(t *testing.T) {
		t.Parallel()
		cfg := filepath.Join(t.TempDir(), "cfg")
		r := agentIn(t, t.TempDir(), nil, enrollArgs(t, h, "--config-dir", cfg)...)
		if r.code != 0 {
			t.Fatalf("enroll = %d: %s", r.code, r.stderr)
		}
		if p := readPolicy(t, cfg).Policy; p.AllowedRepoRoots == nil || len(p.AllowedRepoRoots) != 0 {
			t.Errorf("allowed_repo_roots = %#v, want a stated empty list", p.AllowedRepoRoots)
		}
		if !strings.Contains(r.stderr, "no repos folder") {
			t.Errorf("stderr %q does not say the folder is empty", r.stderr)
		}
	})
	t.Run("the default is ~/.config/fleet-agent", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		r := agentIn(t, home, nil, enrollArgs(t, h)...)
		if r.code != 0 {
			t.Fatalf("enroll = %d: %s", r.code, r.stderr)
		}
		readPolicy(t, filepath.Join(home, ".config", "fleet-agent"))
	})
	t.Run("an absolute XDG_CONFIG_HOME", func(t *testing.T) {
		t.Parallel()
		xdg := t.TempDir()
		home := t.TempDir()
		r := agentIn(t, home, []string{"XDG_CONFIG_HOME=" + xdg}, enrollArgs(t, h)...)
		if r.code != 0 {
			t.Fatalf("enroll = %d: %s", r.code, r.stderr)
		}
		readPolicy(t, filepath.Join(xdg, "fleet-agent"))
		if _, err := os.Stat(filepath.Join(home, ".config")); err == nil {
			t.Error("the default folder was made although XDG_CONFIG_HOME was set")
		}
	})
	t.Run("a relative XDG_CONFIG_HOME is ignored", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		r := agentIn(t, home, []string{"XDG_CONFIG_HOME=relative/dir"}, enrollArgs(t, h)...)
		if r.code != 0 {
			t.Fatalf("enroll = %d: %s", r.code, r.stderr)
		}
		readPolicy(t, filepath.Join(home, ".config", "fleet-agent"))
	})
}

func TestEnrollNeverReplacesAnExistingPolicyFile(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	cfg := mkdir(t, filepath.Join(t.TempDir(), "cfg"))
	const mine = "schema_version: \"1\"\nmax_active: 5\n"
	if err := os.WriteFile(filepath.Join(cfg, policy.File), []byte(mine), 0o600); err != nil {
		t.Fatal(err)
	}
	r := agentIn(t, t.TempDir(), nil, append(enrollArgs(t, h, "--config-dir", cfg), "--repos", t.TempDir())...)
	if r.code != 0 {
		t.Fatalf("enroll = %d: %s", r.code, r.stderr)
	}
	if got, _ := os.ReadFile(filepath.Join(cfg, policy.File)); string(got) != mine { //nolint:gosec // G304: a test folder
		t.Errorf("policy.yaml = %q, want it untouched", got)
	}
	if !strings.Contains(r.stderr, "kept your existing") {
		t.Errorf("stderr %q does not say the file was kept", r.stderr)
	}
}

func TestABadReposFolderStopsEnrollBeforeTheHubIsReached(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	file := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, repos := range []string{filepath.Join(t.TempDir(), "missing"), file, "relative/path"} {
		args := enrollArgs(t, h, "--config-dir", filepath.Join(t.TempDir(), "cfg"))
		before := h.Requests()
		r := agentIn(t, t.TempDir(), nil, append(args, "--repos", repos)...)
		if r.code != 1 || !strings.Contains(r.stderr, "--repos:") {
			t.Errorf("--repos %q: exit %d, stderr %q; want 1 naming --repos", repos, r.code, r.stderr)
		}
		if h.Requests() != before {
			t.Errorf("--repos %q: the hub was reached; a token must not be spent on a typo", repos)
		}
	}
}

func TestAFailedEnrollWritesNoPolicyFile(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	cfg := filepath.Join(t.TempDir(), "cfg")
	state := filepath.Join(t.TempDir(), "state")
	r := agentIn(t, t.TempDir(), nil, "--state-dir", state, "--config-dir", cfg, "enroll", "--hub", h.Addr,
		"--token", h.Token(t, time.Now().Add(-time.Minute)), "--repos", t.TempDir())
	if r.code != 1 {
		t.Fatalf("enroll = %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Error("the config folder was made although enroll failed")
	}
	if _, err := conn.LoadIdentity(state); err == nil {
		t.Error("enrolled")
	}
}

// The machine is enrolled once the hub says so; a starter file that cannot be
// written is a warning, not a rollback, and the exit code stays 0.
func TestAStarterFileThatCannotBeWrittenIsAWarningAfterASuccessfulEnroll(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root can write into a read-only folder")
	}
	h := conntest.StartHub(t, conntest.Options{})
	cfg := mkdir(t, filepath.Join(t.TempDir(), "cfg"))
	if err := os.Chmod(cfg, 0o500); err != nil { //nolint:gosec // G302: a read-only test folder
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg, 0o700) }) //nolint:gosec // G302: lets the test folder be removed
	args := enrollArgs(t, h, "--config-dir", cfg)
	r := agentIn(t, t.TempDir(), nil, args...)
	if r.code != 0 {
		t.Fatalf("enroll = %d: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "fleet-agent: warning: could not write "+filepath.Join(cfg, policy.File)) {
		t.Errorf("stderr %q has no warning naming the file", r.stderr)
	}
	if _, err := conn.LoadIdentity(args[1]); err != nil {
		t.Errorf("the machine is not enrolled: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg, policy.File)); err == nil {
		t.Error("a policy file exists")
	}
}

func TestReposIsCheckedEvenWhenAPolicyFileExists(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	cfg := mkdir(t, filepath.Join(t.TempDir(), "cfg"))
	if err := os.WriteFile(filepath.Join(cfg, policy.File), []byte("schema_version: \"1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := h.Requests()
	r := agentIn(t, t.TempDir(), nil, append(enrollArgs(t, h, "--config-dir", cfg), "--repos", filepath.Join(t.TempDir(), "missing"))...)
	if r.code != 1 || !strings.Contains(r.stderr, "--repos:") || h.Requests() != before {
		t.Errorf("exit %d, stderr %q, hub reached %v; want 1 naming --repos and the hub untouched", r.code, r.stderr, h.Requests() != before)
	}
}
