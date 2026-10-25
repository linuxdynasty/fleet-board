package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// Runner runs one git CLI call. *proc.Runner satisfies it; the seam keeps
// tests on the production path (real git in temp checkouts).
type Runner interface {
	Run(ctx context.Context, sp proc.Spec) (proc.Result, error)
}

// Config wires a Manager. Roots is the local policy's allowed repo roots,
// the ceiling for every path this package touches. LookupRepo maps a short
// RepoRef to its main-checkout top (the repo inventory owns the production
// table). InUse reports a live pane working inside a path (the goal
// registry owns pane bindings); nil means none. ReleaseByPath drops
// registry entries pointing into a pruned worktree; nil means skip. The
// registry lane wires the production seams.
type Config struct {
	Runner        Runner
	Caller        proc.Caller
	Roots         func() []string
	LookupRepo    func(repo *typev1.RepoRef) (string, error)
	InUse         func(path string) bool
	ReleaseByPath func(path string)
	StateDir      func() string
	Now           func() time.Time
}

// Manager runs EnsureWorktree/PruneWorktree (H2) and EnsureScratch/
// CollectScratch (F7). One goroutine owns each call; git serializes the
// worktree mutations itself.
type Manager struct {
	runner  Runner
	caller  proc.Caller
	roots   func() []string
	lookup  func(repo *typev1.RepoRef) (string, error)
	inUse   func(path string) bool
	release func(path string)
	state   func() string
	now     func() time.Time
}

// New checks the setup and fails fast: a half-built Manager must never
// answer a command.
func New(cfg Config) (*Manager, error) {
	if cfg.Runner == nil {
		return nil, errors.New("worktree: Runner is required")
	}
	if cfg.Caller.Name() == "" {
		return nil, errors.New("worktree: Caller is required (proc.For(\"git\"))")
	}
	if cfg.Roots == nil {
		return nil, errors.New("worktree: Roots is required")
	}
	if cfg.LookupRepo == nil {
		return nil, errors.New("worktree: LookupRepo is required")
	}
	if cfg.StateDir == nil {
		return nil, errors.New("worktree: StateDir is required")
	}
	m := &Manager{
		runner: cfg.Runner, caller: cfg.Caller, roots: cfg.Roots,
		lookup: cfg.LookupRepo, inUse: cfg.InUse,
		release: cfg.ReleaseByPath, state: cfg.StateDir, now: cfg.Now,
	}
	if m.inUse == nil {
		m.inUse = func(string) bool { return false }
	}
	if m.now == nil {
		m.now = time.Now
	}
	return m, nil
}

// refuse builds a refusal: nothing was done, or (FAILED) the attempt failed.
func refuse(st typev1.Status, why typev1.ReasonCode, msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: st, Reason: why, Message: msg}
}

// refuseWhy adds the closed "why" marker Triage names (01d2).
func refuseWhy(st typev1.Status, why typev1.ReasonCode, msg, marker string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: st, Reason: why, Message: msg, Metadata: map[string]string{"why": marker}}
}

// git runs one git call in dir; stdout, stderr and the error come back
// together, so failures map on stderr, never on a bare exit status.
func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, string, error) {
	res, err := m.runner.Run(ctx, proc.Spec{Caller: m.caller, Path: "git", Args: args, Dir: dir})
	return string(res.Stdout), string(res.Stderr), err
}

// exited reports whether err is a child that ran and exited non-zero (as
// opposed to a runner failure): only then does stderr decide the mapping.
func exited(err error) bool {
	var ee *proc.ExitError
	return errors.As(err, &ee)
}

// underRoots reports whether dir sits under a configured root. Both sides
// are symlink-resolved first; no roots configured means nowhere is allowed.
func (m *Manager) underRoots(dir string) bool {
	roots := m.roots()
	if len(roots) == 0 {
		return false
	}
	clean, err := filepath.EvalSymlinks(dir)
	if err != nil {
		clean = filepath.Clean(dir)
	}
	for _, r := range roots {
		base, err := filepath.EvalSymlinks(r)
		if err != nil {
			base = filepath.Clean(r)
		}
		edge := strings.TrimSuffix(base, string(filepath.Separator)) + string(filepath.Separator)
		if clean == base || strings.HasPrefix(clean, edge) {
			return true
		}
	}
	return false
}

// samePath compares two paths after resolving symlinks where possible.
func samePath(a, b string) bool {
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	if ea == nil && eb == nil {
		return ra == rb
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// checkoutTop resolves a RepoRef to its canonical checkout top: the lookup
// table names it, git confirms it is a checkout. Anything else is
// CHECKOUT_MISSING: the hub retries elsewhere.
func (m *Manager) checkoutTop(ctx context.Context, repo *typev1.RepoRef) (string, *agentv1.CommandResult) {
	top, err := m.lookup(repo)
	if err != nil || top == "" {
		return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING,
			"no local checkout of that repo")
	}
	out, _, err := m.git(ctx, top, "--no-optional-locks", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING,
			"no local checkout of that repo")
	}
	top = strings.TrimSpace(out)
	if !m.underRoots(top) {
		return "", refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
			"the checkout is under no allowed repo root")
	}
	return top, nil
}

// worktreeEntry is one linked worktree from `git worktree list --porcelain`.
type worktreeEntry struct {
	path   string
	branch string // short name, "" when detached or bare
}

// listWorktrees parses `git worktree list --porcelain` in top.
func (m *Manager) listWorktrees(ctx context.Context, top string) ([]worktreeEntry, *agentv1.CommandResult) {
	out, _, err := m.git(ctx, top, "--no-optional-locks", "worktree", "list", "--porcelain")
	if err != nil {
		return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GIT_FAILED,
			"git worktree list failed")
	}
	var entries []worktreeEntry
	var cur *worktreeEntry
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			entries = append(entries, worktreeEntry{path: strings.TrimSpace(strings.TrimPrefix(line, "worktree "))})
			cur = &entries[len(entries)-1]
		case strings.HasPrefix(line, "branch ") && cur != nil:
			cur.branch = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "branch ")), "refs/heads/")
		}
	}
	return entries, nil
}

// findEntry returns the listed worktree at path, if any.
func findEntry(entries []worktreeEntry, path string) *worktreeEntry {
	for i := range entries {
		if samePath(entries[i].path, path) {
			return &entries[i]
		}
	}
	return nil
}

// branchState reads HEAD and dirtiness in a worktree for the result.
func (m *Manager) branchState(ctx context.Context, repo *typev1.RepoRef, goal, branch, path string) (*agentv1.BranchState, *agentv1.CommandResult) {
	head, _, err := m.git(ctx, path, "--no-optional-locks", "rev-parse", "HEAD")
	if err != nil {
		return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GIT_FAILED,
			"git rev-parse HEAD failed")
	}
	status, _, err := m.git(ctx, path, "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return nil, refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GIT_FAILED,
			"git status failed")
	}
	return &agentv1.BranchState{
		Repo: repo, Path: path, GoalId: &typev1.GoalId{Value: goal},
		Branch: branch, HeadCommit: strings.TrimSpace(head),
		Dirty: len(status) > 0, UpstreamUnknown: true, ObservedTime: timestamppb.New(m.now()),
	}, nil
}

// boardResult maps an EnsureBoardDir failure onto GOAL_FILE_WRITE_FAILED
// with the closed why marker (01d2).
func boardResult(err error) *agentv1.CommandResult {
	switch {
	case errors.Is(err, ErrDirNotPlain):
		return refuseWhy(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GOAL_FILE_WRITE_FAILED,
			"the .fleet-board folder is not a plain directory", "goal_dir_not_plain")
	case errors.Is(err, ErrDirForeign):
		return refuseWhy(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GOAL_FILE_WRITE_FAILED,
			"the .fleet-board marker is foreign", "goal_dir_foreign")
	default:
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GOAL_FILE_WRITE_FAILED,
			"cannot create the .fleet-board folder")
	}
}

// EnsureWorktree creates the goal's worktree (H2). Declarative: an existing
// worktree on the same branch is OK with created=false; a different branch
// or other content at that path is WORKTREE_EXISTS. The default path is a
// sibling of the main checkout, so the developer's checkout gains no
// untracked files. Fresh worktrees get the board bootstrap (01d2).
func (m *Manager) EnsureWorktree(ctx context.Context, cmd *agentv1.EnsureWorktree) *agentv1.CommandResult {
	goal := cmd.GetGoalId().GetValue()
	if !validateGoalID(goal) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"goal id must match the 02e id format")
	}
	branch := cmd.GetBranch()
	if !validateBranch(branch) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"branch must be a valid git branch name")
	}
	top, bad := m.checkoutTop(ctx, cmd.GetRepo())
	if bad != nil {
		return bad
	}
	if base := cmd.GetBaseRef(); base != "" {
		if _, _, err := m.git(ctx, top, "--no-optional-locks", "rev-parse", "--verify", "--quiet", base); err != nil {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"unknown base ref")
		}
	}
	var path string
	if rel := cmd.GetRelativePath(); rel == "" {
		path = filepath.Join(filepath.Dir(top), filepath.Base(top)+"--"+goal)
	} else {
		joined, ok := joinRelative(top, rel)
		if !ok {
			return refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
				"the work path climbs out of its checkout")
		}
		path = joined
	}
	if !m.underRoots(path) {
		return refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
			"the worktree is under no allowed repo root")
	}
	entries, bad := m.listWorktrees(ctx, top)
	if bad != nil {
		return bad
	}
	if found := findEntry(entries, path); found != nil {
		if found.branch != branch {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_WORKTREE_EXISTS,
				"that path holds a worktree on another branch")
		}
		return m.existingWorktree(ctx, cmd.GetRepo(), goal, branch, found.path, false)
	}
	if _, err := os.Lstat(path); err == nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_WORKTREE_EXISTS,
			"that path is already taken")
	}
	if _, _, err := m.git(ctx, top, "--no-optional-locks", "show-ref", "--verify", "refs/heads/"+branch); err != nil && !exited(err) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GIT_FAILED,
			"git show-ref failed")
	} else if err == nil {
		if _, serr, err := m.git(ctx, top, "--no-optional-locks", "worktree", "add", path, branch); err != nil {
			return m.mapAddFailure(serr, err)
		}
	} else if base := cmd.GetBaseRef(); base != "" {
		if _, serr, err := m.git(ctx, top, "--no-optional-locks", "worktree", "add", "-b", branch, path, base); err != nil {
			return m.mapAddFailure(serr, err)
		}
	} else if _, serr, err := m.git(ctx, top, "--no-optional-locks", "worktree", "add", "-b", branch, path); err != nil {
		return m.mapAddFailure(serr, err)
	}
	return m.existingWorktree(ctx, cmd.GetRepo(), goal, branch, path, true)
}

// existingWorktree bootstraps the board folder and reports the worktree.
func (m *Manager) existingWorktree(ctx context.Context, repo *typev1.RepoRef, goal, branch, path string, created bool) *agentv1.CommandResult {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = filepath.Clean(path)
	}
	if _, _, err := EnsureBoardDir(real); err != nil {
		return boardResult(err)
	}
	state, bad := m.branchState(ctx, repo, goal, branch, real)
	if bad != nil {
		return bad
	}
	verb := "reused worktree at"
	if created {
		verb = "created worktree at"
	}
	return &agentv1.CommandResult{
		Status: typev1.Status_STATUS_OK, Reason: typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
		Message: fmt.Sprintf("%s %s on branch %s", verb, real, branch),
		Detail:  &agentv1.CommandResult_EnsureWorktree{EnsureWorktree: &agentv1.EnsureWorktreeResult{Path: real, Created: created, Branch: state}},
	}
}

// mapAddFailure reads a failed `git worktree add`: a taken branch or path
// is WORKTREE_EXISTS, anything else is GIT_FAILED.
func (m *Manager) mapAddFailure(serr string, err error) *agentv1.CommandResult {
	if exited(err) && (strings.Contains(serr, "already checked out") ||
		strings.Contains(serr, "already used") ||
		strings.Contains(serr, "already exists")) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_WORKTREE_EXISTS,
			"that branch or path is already in use")
	}
	return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GIT_FAILED,
		"git worktree add failed")
}

// PruneWorktree removes a goal worktree after merge and grace (H2).
// WORKTREE_DIRTY unless allow_dirty; WORKTREE_IN_USE when a live pane
// works inside. Only a worktree git lists is ever removed: a plain folder
// or a missing path is a no-op OK, and the repo top itself is refused.
// Pruning drops the registry entries that point into the path (Config).
func (m *Manager) PruneWorktree(ctx context.Context, cmd *agentv1.PruneWorktree) *agentv1.CommandResult {
	raw := filepath.Clean(cmd.GetPath())
	if cmd.GetPath() == "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a worktree path is required")
	}
	real, err := filepath.EvalSymlinks(raw)
	if err != nil {
		real = raw
	}
	if !m.underRoots(real) {
		return refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
			"the worktree is under no allowed repo root")
	}
	top, bad := m.checkoutTop(ctx, cmd.GetRepo())
	if bad != nil {
		return bad
	}
	if samePath(top, real) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the repo top itself is never pruned")
	}
	entries, bad := m.listWorktrees(ctx, top)
	if bad != nil {
		return bad
	}
	found := findEntry(entries, real)
	if found == nil {
		return refuse(typev1.Status_STATUS_OK, typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
			"no worktree at that path")
	}
	if m.inUse(real) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_WORKTREE_IN_USE,
			"a live pane works in that worktree")
	}
	if status, _, err := m.git(ctx, real, "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=normal"); err != nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GIT_FAILED,
			"git status failed")
	} else if len(status) > 0 && !cmd.GetAllowDirty() {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_WORKTREE_DIRTY,
			"the worktree has uncommitted changes")
	}
	args := []string{"--no-optional-locks", "worktree", "remove"}
	if cmd.GetAllowDirty() {
		args = append(args, "--force")
	}
	if _, _, err := m.git(ctx, top, append(args, real)...); err != nil {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GIT_FAILED,
			"git worktree remove failed")
	}
	if cmd.GetDeleteBranch() && found.branch != "" {
		if _, _, err := m.git(ctx, top, "--no-optional-locks", "show-ref", "--verify", "--quiet", "refs/heads/"+found.branch); err == nil {
			if _, _, err := m.git(ctx, top, "--no-optional-locks", "branch", "-D", found.branch); err != nil {
				return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GIT_FAILED,
					"git branch delete failed")
			}
		} else if !exited(err) {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_GIT_FAILED,
				"git show-ref failed")
		}
	}
	if m.release != nil {
		m.release(real)
	}
	return refuse(typev1.Status_STATUS_OK, typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
		fmt.Sprintf("pruned worktree at %s", real))
}
