package repos

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// Remote is one git remote with its normalized repo (02h "Repo identity").
// The URL never leaves the machine: only Name, Host and Path are reported.
type Remote struct {
	Name string
	Host string
	Path string
}

// Detail is what one checkout reports (H1, H3): its top, branch and HEAD,
// whether it is dirty (tracked or untracked changes alike), its upstream
// position, its remotes and its worktree shape.
type Detail struct {
	Top             string
	Branch          string // empty on a detached HEAD
	Head            string
	Dirty           bool
	UpstreamUnknown bool
	Ahead           uint32
	Behind          uint32
	Remotes         []Remote
	RemoteName      string // remote RepoRef was derived from (policy or origin)
	IsWorktree      bool
	MainCheckout    string // set for linked worktrees
	DefaultBranch   string // of RemoteName, empty when unknown
}

// Git inspects checkouts. Implementations run real git (through
// internal/proc, caller "git") or a stub for tests.
type Git interface {
	Inspect(ctx context.Context, path string) (Detail, error)
	// DefaultBranch reads refs/remotes/<remote>/HEAD: "" when the clone
	// never learned it. Never an error for that case.
	DefaultBranch(ctx context.Context, top, remote string) string
}

type realGit struct {
	runner *proc.Runner
	caller proc.Caller
}

// NewGit runs git through the runner with the named caller ("git").
func NewGit(runner *proc.Runner, caller proc.Caller) Git {
	return realGit{runner: runner, caller: caller}
}

func (g realGit) run(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := g.runner.Run(ctx, proc.Spec{Caller: g.caller, Path: "git", Args: args, Dir: dir})
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

// Inspect reads one checkout with read-only git commands. A folder that is
// not a checkout, or a git that fails, is an error: the caller keeps its
// last reading rather than guessing.
func (g realGit) Inspect(ctx context.Context, path string) (Detail, error) {
	var d Detail
	top, err := g.run(ctx, path, "--no-optional-locks", "rev-parse", "--show-toplevel")
	if err != nil {
		return Detail{}, fmt.Errorf("repos: git top: %w", err)
	}
	d.Top = strings.TrimSpace(top)
	run := func(args ...string) (string, error) { return g.run(ctx, d.Top, args...) }

	branch, err := run("--no-optional-locks", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return Detail{}, fmt.Errorf("repos: git branch: %w", err)
	}
	if b := strings.TrimSpace(branch); b != "HEAD" {
		d.Branch = b
	}
	head, err := run("--no-optional-locks", "rev-parse", "HEAD")
	if err != nil {
		return Detail{}, fmt.Errorf("repos: git head: %w", err)
	}
	d.Head = strings.TrimSpace(head)
	status, err := run("--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=normal")
	if err != nil {
		return Detail{}, fmt.Errorf("repos: git status: %w", err)
	}
	d.Dirty = len(status) > 0
	if _, err := run("--no-optional-locks", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		d.UpstreamUnknown = true
	} else if counts, err := run("--no-optional-locks", "rev-list", "--count", "--left-right", "@{upstream}...HEAD"); err != nil {
		d.UpstreamUnknown = true
	} else if behind, ahead, ok := splitCounts(counts); ok {
		d.Behind, d.Ahead = behind, ahead
	} else {
		d.UpstreamUnknown = true
	}
	names, err := run("--no-optional-locks", "remote")
	if err != nil {
		return Detail{}, fmt.Errorf("repos: git remotes: %w", err)
	}
	for _, name := range strings.Fields(names) {
		raw, err := run("--no-optional-locks", "remote", "get-url", name)
		if err != nil {
			continue // a remote with no URL is skipped, never fatal
		}
		host, repo, ok := NormalizeRemote(strings.TrimSpace(raw))
		if !ok {
			continue // a local path remote is not a repo
		}
		d.Remotes = append(d.Remotes, Remote{Name: name, Host: host, Path: repo})
	}
	gitDir, err := run("--no-optional-locks", "rev-parse", "--git-dir")
	if err != nil {
		return Detail{}, fmt.Errorf("repos: git dir: %w", err)
	}
	commonDir, err := run("--no-optional-locks", "rev-parse", "--git-common-dir")
	if err != nil {
		return Detail{}, fmt.Errorf("repos: git common dir: %w", err)
	}
	// Either may print relative to the checkout top; the top is known, so
	// both are made absolute before they are compared.
	gitDir, commonDir = absTop(d.Top, gitDir), absTop(d.Top, commonDir)
	if d.IsWorktree = gitDir != commonDir; d.IsWorktree {
		d.MainCheckout = filepath.Dir(commonDir)
	}
	return d, nil
}

// absTop resolves a rev-parse dir (absolute, or relative to the checkout
// top) to an absolute path.
func absTop(top, dir string) string {
	dir = strings.TrimSpace(dir)
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	return filepath.Join(top, dir)
}

// DefaultBranch reads the remote's HEAD branch ("origin/main" trims to
// "main"). Anything unreadable is "", never an error.
func (g realGit) DefaultBranch(ctx context.Context, top, remote string) string {
	if remote == "" {
		return ""
	}
	out, err := g.run(ctx, top, "--no-optional-locks", "symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(out), remote+"/")
}

// splitCounts reads `rev-list --left-right --count`: "<behind>\t<ahead>".
func splitCounts(s string) (behind, ahead uint32, ok bool) {
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return 0, 0, false
	}
	b, err1 := strconv.ParseUint(parts[0], 10, 32)
	a, err2 := strconv.ParseUint(parts[1], 10, 32)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return uint32(b), uint32(a), true
}
