package worktree_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/worktree"
)

type repoTable map[string]string

func testManager(t *testing.T, roots []string, lookup repoTable) (*worktree.Manager, *[]string) {
	t.Helper()
	var released []string
	cfg := testConfig(t, roots, lookup, t.TempDir())
	cfg.ReleaseByPath = func(path string) { released = append(released, path) }
	m, err := worktree.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m, &released
}

func testManagerInUse(t *testing.T, roots []string, lookup repoTable, inUse func(string) bool) *worktree.Manager {
	t.Helper()
	cfg := testConfig(t, roots, lookup, t.TempDir())
	cfg.InUse = inUse
	m, err := worktree.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func repoRef() *typev1.RepoRef { return &typev1.RepoRef{Host: "github.com", Path: "acme/repo"} }

func ensureCmd(goal, branch, base, rel string) *agentv1.EnsureWorktree {
	return &agentv1.EnsureWorktree{
		Repo:         repoRef(),
		GoalId:       &typev1.GoalId{Value: goal},
		Branch:       branch,
		BaseRef:      base,
		RelativePath: rel,
	}
}

func ensureResult(t *testing.T, res *agentv1.CommandResult) *agentv1.EnsureWorktreeResult {
	t.Helper()
	d, ok := res.GetDetail().(*agentv1.CommandResult_EnsureWorktree)
	if !ok || d.EnsureWorktree == nil {
		t.Fatalf("result carries no EnsureWorktree detail: %v", res)
	}
	return d.EnsureWorktree
}

func headAndStatus(t *testing.T, dir string) (string, string) {
	t.Helper()
	head := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	status := gitRun(t, dir, "status", "--porcelain=v1", "--untracked-files=normal")
	return head, status
}

// realTop resolves the fixture's symlinks (macOS /var): the manager reports
// real paths, so the test compares real paths.
func realTop(t *testing.T) string {
	t.Helper()
	top, err := filepath.EvalSymlinks(gitRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	return top
}

func TestEnsureWorktree(t *testing.T) {
	t.Parallel()
	needGit(t)
	cases := []struct {
		name       string
		branch     string
		base       string
		rel        string
		goal       string
		lookup     bool // false: the repo is unknown
		rootsOK    bool // false: roots do not cover the checkout
		wantStatus typev1.Status
		wantReason typev1.ReasonCode
	}{
		{name: "creates sibling worktree", branch: "fleet/w1", goal: "goal1", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_OK},
		{name: "creates nested worktree", branch: "fleet/w2", rel: "wt/w2", goal: "goal2", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_OK},
		{name: "existing branch at a new path", branch: "main", base: "", rel: "wt/from-main", goal: "goal12", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_FAILED, wantReason: typev1.ReasonCode_REASON_CODE_WORKTREE_EXISTS},
		{name: "empty branch refused", goal: "goal3", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_FAILED, wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{name: "dotdot branch refused", branch: "bad..name", goal: "goal4", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_FAILED, wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{name: "traversal refused", branch: "fleet/w5", rel: "../evil", goal: "goal5", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_REJECTED_BY_POLICY, wantReason: typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED},
		{name: "absolute path refused", branch: "fleet/w6", rel: "/tmp/evil", goal: "goal6", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_REJECTED_BY_POLICY, wantReason: typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED},
		{name: "quoted path refused", branch: "fleet/wq", rel: "wt/\"q\"", goal: "goalq", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_REJECTED_BY_POLICY, wantReason: typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED},
		{name: "unknown repo", branch: "fleet/w7", goal: "goal7", rootsOK: true, wantStatus: typev1.Status_STATUS_FAILED, wantReason: typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING},
		{name: "roots refuse the checkout", branch: "fleet/w8", goal: "goal8", lookup: true, wantStatus: typev1.Status_STATUS_REJECTED_BY_POLICY, wantReason: typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED},
		{name: "unknown base refused", branch: "fleet/w9", base: "no-such-ref", goal: "goal9", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_FAILED, wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{name: "bad goal id refused", branch: "fleet/w10", goal: "Bad_Id!", lookup: true, rootsOK: true, wantStatus: typev1.Status_STATUS_FAILED, wantReason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			top := realTop(t)
			lookup := repoTable{}
			if tc.lookup {
				lookup["github.com/acme/repo"] = top
			}
			roots := []string{filepath.Dir(top)}
			if !tc.rootsOK {
				roots = []string{t.TempDir()}
			}
			m, _ := testManager(t, roots, lookup)
			beforeHead, beforeStatus := headAndStatus(t, top)

			res := m.EnsureWorktree(ctx, ensureCmd(tc.goal, tc.branch, tc.base, tc.rel))
			if res.GetStatus() != tc.wantStatus || res.GetReason() != tc.wantReason {
				t.Fatalf("status = %v/%v, want %v/%v (%s)", res.GetStatus(), res.GetReason(), tc.wantStatus, tc.wantReason, res.GetMessage())
			}
			if tc.wantStatus != typev1.Status_STATUS_OK {
				if _, err := os.Stat(filepath.Join(filepath.Dir(top), "evil")); err == nil {
					t.Fatal("traversal created a directory")
				}
				return
			}
			got := ensureResult(t, res)
			if !got.GetCreated() {
				t.Fatal("first ensure reads created=false")
			}
			real, err := filepath.EvalSymlinks(got.GetPath())
			if err != nil {
				t.Fatal(err)
			}
			if tc.rel == "" && filepath.Dir(real) == top {
				t.Fatalf("default worktree sits inside the checkout: %s", real)
			}
			if tc.rel != "" && real != filepath.Join(top, filepath.Clean(tc.rel)) {
				t.Fatalf("path = %s, want it under %s", real, tc.rel)
			}
			if got.GetBranch().GetBranch() != tc.branch {
				t.Fatalf("branch = %q, want %q", got.GetBranch().GetBranch(), tc.branch)
			}
			if len(got.GetBranch().GetHeadCommit()) != 40 {
				t.Fatalf("head = %q, want a full sha", got.GetBranch().GetHeadCommit())
			}
			// The fresh worktree carries the board bootstrap.
			if raw, err := os.ReadFile(filepath.Join(real, ".fleet-board", ".gitignore")); err != nil || !strings.Contains(string(raw), "\n*\n") {
				t.Fatalf("board bootstrap missing in %s: %v %q", real, err, raw)
			}
			// Declarative: the same call again is OK with created=false.
			again := m.EnsureWorktree(ctx, ensureCmd(tc.goal, tc.branch, tc.base, tc.rel))
			if again.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("second ensure: %v", again)
			}
			againRes := ensureResult(t, again)
			if againRes.GetCreated() {
				t.Fatalf("second ensure reads created=true: %v", again)
			}
			if againRes.GetPath() != got.GetPath() {
				t.Fatalf("second ensure moved the path: %q vs %q", againRes.GetPath(), got.GetPath())
			}
			// The developer checkout is untouched: same HEAD, and no change
			// to tracked files. A nested worktree (an explicit relative
			// path, work.proto's .worktrees/ example) shows as untracked
			// content in the main checkout; that is the caller's choice,
			// not a modification.
			afterHead, afterStatus := headAndStatus(t, top)
			if afterHead != beforeHead {
				t.Fatalf("developer checkout HEAD changed: %s->%s", beforeHead, afterHead)
			}
			if tc.rel == "" {
				if afterStatus != beforeStatus {
					t.Fatalf("developer checkout changed: status %q->%q", beforeStatus, afterStatus)
				}
			} else {
				for _, line := range strings.Split(strings.TrimSuffix(afterStatus, "\n"), "\n") {
					if line != "" && !strings.HasPrefix(line, "?? ") {
						t.Fatalf("developer checkout tracked files changed: %q", afterStatus)
					}
				}
			}
		})
	}
}

func TestEnsureWorktreeConflicts(t *testing.T) {
	t.Parallel()
	needGit(t)
	ctx := context.Background()

	t.Run("different branch at the same path", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		m, _ := testManager(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top})
		if res := m.EnsureWorktree(ctx, ensureCmd("goala", "fleet/a", "", "wt/x")); res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("setup ensure: %v", res)
		}
		res := m.EnsureWorktree(ctx, ensureCmd("goalb", "fleet/b", "", "wt/x"))
		if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_WORKTREE_EXISTS {
			t.Fatalf("status = %v/%v, want FAILED/WORKTREE_EXISTS", res.GetStatus(), res.GetReason())
		}
	})

	t.Run("plain directory at the path", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		if err := os.MkdirAll(filepath.Join(top, "wt", "taken"), 0o700); err != nil {
			t.Fatal(err)
		}
		m, _ := testManager(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top})
		res := m.EnsureWorktree(ctx, ensureCmd("goald", "fleet/d", "", "wt/taken"))
		if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_WORKTREE_EXISTS {
			t.Fatalf("status = %v/%v, want FAILED/WORKTREE_EXISTS", res.GetStatus(), res.GetReason())
		}
	})

	t.Run("not a checkout", func(t *testing.T) {
		t.Parallel()
		plain := t.TempDir()
		m, _ := testManager(t, []string{filepath.Dir(plain)}, repoTable{"github.com/acme/repo": plain})
		res := m.EnsureWorktree(ctx, ensureCmd("goale", "fleet/e", "", ""))
		if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING {
			t.Fatalf("status = %v/%v, want FAILED/CHECKOUT_MISSING", res.GetStatus(), res.GetReason())
		}
	})

	t.Run("foreign board in the same worktree", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		gitRun(t, top, "worktree", "add", filepath.Join(top, "wt", "foreign"), "-b", "fleet/foreign")
		wt, err := filepath.EvalSymlinks(filepath.Join(top, "wt", "foreign"))
		if err != nil {
			t.Fatal(err)
		}
		board := filepath.Join(wt, ".fleet-board")
		if err := os.MkdirAll(board, 0o700); err != nil {
			t.Fatal(err)
		}
		// A marker the helper did not create stays untouched.
		if err := os.WriteFile(filepath.Join(board, ".gitignore"), []byte("mine\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		m, _ := testManager(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top})
		res := m.EnsureWorktree(ctx, ensureCmd("goalf", "fleet/foreign", "", "wt/foreign"))
		if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_GOAL_FILE_WRITE_FAILED {
			t.Fatalf("status = %v/%v, want FAILED/GOAL_FILE_WRITE_FAILED", res.GetStatus(), res.GetReason())
		}
		if res.GetMetadata()["why"] != "goal_dir_foreign" {
			t.Fatalf("metadata = %v, want why=goal_dir_foreign", res.GetMetadata())
		}
		if raw, err := os.ReadFile(filepath.Join(board, ".gitignore")); err != nil || string(raw) != "mine\n" { //nolint:gosec // G304: the test's own board folder
			t.Fatalf("foreign marker changed: %q %v", raw, err)
		}
	})
}

func pruneCmd(goal, path string, allowDirty, deleteBranch bool) *agentv1.PruneWorktree {
	return &agentv1.PruneWorktree{
		Repo:         repoRef(),
		GoalId:       &typev1.GoalId{Value: goal},
		Path:         path,
		AllowDirty:   allowDirty,
		DeleteBranch: deleteBranch,
	}
}

func worktreePaths(t *testing.T, top string) string {
	t.Helper()
	return gitRun(t, top, "worktree", "list", "--porcelain")
}

func TestPruneWorktree(t *testing.T) {
	t.Parallel()
	needGit(t)
	ctx := context.Background()

	t.Run("ensure then prune removes it all", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		m, released := testManager(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top})
		beforeHead, beforeStatus := headAndStatus(t, top)

		res := m.EnsureWorktree(ctx, ensureCmd("goalp1", "fleet/p1", "", ""))
		if res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("ensure: %v", res)
		}
		path := ensureResult(t, res).GetPath()
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}

		prune := m.PruneWorktree(ctx, pruneCmd("goalp1", path, false, false))
		if prune.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("prune: %v", prune)
		}
		if prune.GetDetail() != nil {
			t.Fatalf("prune carries a result detail: %v", prune)
		}
		if _, err := os.Stat(real); !os.IsNotExist(err) {
			t.Fatalf("worktree still on disk: %s", real)
		}
		if out := worktreePaths(t, top); strings.Contains(out, real) {
			t.Fatalf("worktree still listed:\n%s", out)
		}
		// The registry lane's seam saw the removal.
		if len(*released) != 1 || (*released)[0] != real {
			t.Fatalf("released = %v, want [%s]", *released, real)
		}
		// Pruning again is a no-op OK: the command is declarative.
		if again := m.PruneWorktree(ctx, pruneCmd("goalp1", path, false, false)); again.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("second prune: %v", again)
		}
		// The developer checkout is untouched: same HEAD, clean status.
		if afterHead, afterStatus := headAndStatus(t, top); afterHead != beforeHead || afterStatus != beforeStatus {
			t.Fatalf("developer checkout changed: head %s->%s status %q->%q", beforeHead, afterHead, beforeStatus, afterStatus)
		}
	})

	t.Run("dirty worktree refused without allow dirty", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		m, _ := testManager(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top})
		res := m.EnsureWorktree(ctx, ensureCmd("goalp2", "fleet/p2", "", ""))
		if res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("ensure: %v", res)
		}
		path := ensureResult(t, res).GetPath()
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(real, "dirty.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		prune := m.PruneWorktree(ctx, pruneCmd("goalp2", path, false, false))
		if prune.GetStatus() != typev1.Status_STATUS_FAILED || prune.GetReason() != typev1.ReasonCode_REASON_CODE_WORKTREE_DIRTY {
			t.Fatalf("status = %v/%v, want FAILED/WORKTREE_DIRTY", prune.GetStatus(), prune.GetReason())
		}
		if _, err := os.Stat(real); err != nil {
			t.Fatalf("refused prune removed the worktree: %v", err)
		}
	})

	t.Run("allow dirty removes and keeps the branch by default", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		m, _ := testManager(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top})
		res := m.EnsureWorktree(ctx, ensureCmd("goalp3", "fleet/p3", "", ""))
		if res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("ensure: %v", res)
		}
		path := ensureResult(t, res).GetPath()
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(real, "dirty.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if prune := m.PruneWorktree(ctx, pruneCmd("goalp3", path, true, false)); prune.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("prune: %v", prune)
		}
		if _, err := os.Stat(real); !os.IsNotExist(err) {
			t.Fatalf("worktree still on disk: %s", real)
		}
	})

	t.Run("delete branch removes the branch", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		m, _ := testManager(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top})
		res := m.EnsureWorktree(ctx, ensureCmd("goalp4", "fleet/p4", "", "wt/p4"))
		if res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("ensure: %v", res)
		}
		path := ensureResult(t, res).GetPath()
		if prune := m.PruneWorktree(ctx, pruneCmd("goalp4", path, false, true)); prune.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("prune: %v", prune)
		}
		if out := gitRun(t, top, "branch", "--list", "fleet/p4"); strings.TrimSpace(out) != "" {
			t.Fatalf("branch still exists: %s", out)
		}
	})

	t.Run("worktree in use refused", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		m := testManagerInUse(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top}, func(string) bool { return true })
		res := m.EnsureWorktree(ctx, ensureCmd("goalp5", "fleet/p5", "", ""))
		if res.GetStatus() != typev1.Status_STATUS_OK {
			t.Fatalf("ensure: %v", res)
		}
		path := ensureResult(t, res).GetPath()
		prune := m.PruneWorktree(ctx, pruneCmd("goalp5", path, false, false))
		if prune.GetStatus() != typev1.Status_STATUS_FAILED || prune.GetReason() != typev1.ReasonCode_REASON_CODE_WORKTREE_IN_USE {
			t.Fatalf("status = %v/%v, want FAILED/WORKTREE_IN_USE", prune.GetStatus(), prune.GetReason())
		}
	})

	t.Run("the repo top itself is never pruned", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		m, _ := testManager(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top})
		prune := m.PruneWorktree(ctx, pruneCmd("goalp6", top, false, false))
		if prune.GetStatus() != typev1.Status_STATUS_FAILED || prune.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
			t.Fatalf("status = %v/%v, want FAILED/INVALID_ARGUMENT", prune.GetStatus(), prune.GetReason())
		}
		if head, _ := headAndStatus(t, top); len(head) != 40 {
			t.Fatalf("repo HEAD unreadable after refused prune: %q", head)
		}
	})

	t.Run("path outside the roots refused", func(t *testing.T) {
		t.Parallel()
		top := realTop(t)
		m, _ := testManager(t, []string{t.TempDir()}, repoTable{"github.com/acme/repo": top})
		prune := m.PruneWorktree(ctx, pruneCmd("goalp7", filepath.Join(top, "wt", "x"), false, false))
		if prune.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY {
			t.Fatalf("status = %v/%v, want REJECTED_BY_POLICY", prune.GetStatus(), prune.GetReason())
		}
	})
}

func TestPruneWorktreeKeepsMainBranch(t *testing.T) {
	t.Parallel()
	needGit(t)
	ctx := context.Background()
	top := realTop(t)
	m, _ := testManager(t, []string{filepath.Dir(top)}, repoTable{"github.com/acme/repo": top})
	res := m.EnsureWorktree(ctx, ensureCmd("goalp8", "fleet/p8", "", ""))
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("ensure: %v", res)
	}
	if prune := m.PruneWorktree(ctx, pruneCmd("goalp8", ensureResult(t, res).GetPath(), false, false)); prune.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("prune: %v", prune)
	}
	// The main checkout still sits on main, and its branch is intact.
	if out := strings.TrimSpace(gitRun(t, top, "rev-parse", "--abbrev-ref", "HEAD")); out != "main" {
		t.Fatalf("main checkout branch = %q", out)
	}
}

func TestEnsureWorktreeRefusesSymlinkAncestor(t *testing.T) {
	t.Parallel()
	needGit(t)
	ctx := context.Background()
	top := realTop(t)
	// Outside the allowed roots: the roots hold only the repo top, so a
	// sibling temp dir is outside even though it shares $TMPDIR.
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(top, "link")); err != nil {
		t.Fatal(err)
	}
	m, _ := testManager(t, []string{top}, repoTable{"github.com/acme/repo": top})
	res := m.EnsureWorktree(ctx, ensureCmd("goalsym", "fleet/sym", "", "link/new"))
	if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED {
		t.Fatalf("EnsureWorktree through a symlinked ancestor = %v %v, want REJECTED_BY_POLICY REPO_ROOT_NOT_ALLOWED (%s)", res.GetStatus(), res.GetReason(), res.GetMessage())
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("worktree escaped the roots: %v", entries)
	}
}
