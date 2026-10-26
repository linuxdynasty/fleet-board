package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

func TestSessionName(t *testing.T) {
	t.Parallel()
	id := &agentv1.SessionTarget{By: &agentv1.SessionTarget_SessionId{
		SessionId: &typev1.SessionId{Value: "sess-1"},
	}}
	name := &agentv1.SessionTarget{By: &agentv1.SessionTarget_Name{Name: "win-1"}}
	if got := sessionName(id, "spec-1"); got != "sess-1" {
		t.Fatalf("sessionName id = %q", got)
	}
	if got := sessionName(name, "spec-1"); got != "win-1" {
		t.Fatalf("sessionName name = %q", got)
	}
	if got := sessionName(nil, "spec-1"); got != "spec-1" {
		t.Fatalf("sessionName empty = %q", got)
	}
}

func TestSpoolPath(t *testing.T) {
	t.Setenv("FLEET_HOOK_SPOOL", "/tmp/custom.jsonl")
	if got := spoolPath("/state"); got != "/tmp/custom.jsonl" {
		t.Fatalf("spoolPath env = %q", got)
	}
	t.Setenv("FLEET_HOOK_SPOOL", "")
	if got := spoolPath("/state"); got != filepath.Join("/state", "hook-events.jsonl") {
		t.Fatalf("spoolPath default = %q", got)
	}
}

func TestRawFileIsAppend0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path, err := rawFile(dir, "goal1")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "raw", "goal-goal1.log") {
		t.Fatalf("rawFile = %q", path)
	}
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("raw mode = %o; want 600", fi.Mode().Perm())
	}
	// A second open appends: pipe-pane's `cat >>` must never see a
	// truncated file.
	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rawFile(dir, "goal1"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the raw file this test wrote in a temp dir
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "first\n" {
		t.Fatalf("raw = %q; want the earlier bytes kept", raw)
	}
}

func TestDiskFreeSource(t *testing.T) {
	t.Parallel()
	free, ok := diskFreeSource(func() string { return t.TempDir() })()
	if !ok || free <= 0 {
		t.Fatalf("diskFree = %d, %v; want positive and ok", free, ok)
	}
	if _, ok := diskFreeSource(func() string {
		return filepath.Join(t.TempDir(), "missing")
	})(); ok {
		t.Fatal("diskFree on a missing dir reads ok")
	}
}

// gitCheckout shells out to real git: the switch helper runs on the
// production path, not a copy of it.
func gitCheckout(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		argv := append([]string{"-C", dir}, args...)
		cmd := exec.CommandContext(t.Context(), "git", argv...) //nolint:gosec // G204: tests run git with table-driven subcommands
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-m", "init")
	return dir
}

func TestGitSwitch(t *testing.T) {
	t.Parallel()
	caller, err := proc.For("git")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	sw := &gitSwitch{runner: runner, caller: caller}
	ctx := context.Background()
	dir := gitCheckout(t)

	head, err := sw.SwitchBranch(ctx, dir, "main", "")
	if err != nil || head == "" {
		t.Fatalf("already on branch = %q, %v", head, err)
	}
	head2, err := sw.SwitchBranch(ctx, dir, "fleet/1", "")
	if err != nil || head2 == "" || head2 != head {
		t.Fatalf("switch = %q, %v; want %q", head2, err, head)
	}
	if err := os.WriteFile(filepath.Join(dir, "dirty"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sw.SwitchBranch(ctx, dir, "fleet/2", ""); err == nil {
		t.Fatal("dirty checkout switched branches")
	} else if got := err.Error(); !strings.Contains(got, "dirty") {
		t.Fatalf("dirty err = %q; want a dirty error", got)
	}
}

type stubTrees struct {
	res *agentv1.CommandResult
}

func (s *stubTrees) EnsureWorktree(_ context.Context, cmd *agentv1.EnsureWorktree) *agentv1.CommandResult {
	return s.res
}

func (s *stubTrees) EnsureScratch(_ context.Context, _ *agentv1.EnsureScratch) *agentv1.CommandResult {
	return s.res
}

func TestEnsureScratchDir(t *testing.T) {
	t.Parallel()
	okRes := &agentv1.CommandResult{Status: typev1.Status_STATUS_OK}
	m := &managerTrees{mgr: &stubTrees{res: okRes}, stateDir: func() string { return "/state" }}
	dir, res := m.EnsureScratchDir(context.Background(), "goal1")
	if res != nil || dir != filepath.Join("/state", "scratch", "goal1") {
		t.Fatalf("scratch = %q, %+v", dir, res)
	}
	bad := &agentv1.CommandResult{Status: typev1.Status_STATUS_FAILED}
	m = &managerTrees{mgr: &stubTrees{res: bad}, stateDir: func() string { return "/state" }}
	if _, res := m.EnsureScratchDir(context.Background(), "goal1"); res != bad {
		t.Fatalf("scratch failure = %+v; want passthrough", res)
	}
}

func TestOkResultMapsSuccessToNil(t *testing.T) {
	t.Parallel()
	if got := okResult(&agentv1.CommandResult{Status: typev1.Status_STATUS_OK}); got != nil {
		t.Fatalf("okResult(OK) = %+v; want nil (nil is the Typer's success)", got)
	}
	failed := &agentv1.CommandResult{Status: typev1.Status_STATUS_FAILED}
	if got := okResult(failed); got != failed {
		t.Fatalf("okResult(FAILED) = %+v; want passthrough", got)
	}
	unknown := &agentv1.CommandResult{Status: typev1.Status_STATUS_UNKNOWN_OUTCOME}
	if got := okResult(unknown); got != unknown {
		t.Fatalf("okResult(UNKNOWN_OUTCOME) = %+v; want passthrough (the runner must not replay it)", got)
	}
	if got := okResult(nil); got != nil {
		t.Fatalf("okResult(nil) = %+v; want nil", got)
	}
}
