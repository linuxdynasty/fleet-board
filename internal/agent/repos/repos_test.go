package repos_test

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
)

type stubGit struct {
	details map[string]repos.Detail
	err     map[string]bool
}

func (s stubGit) Inspect(_ context.Context, path string) (repos.Detail, error) {
	if s.err[path] {
		return repos.Detail{}, errStubGit
	}
	return s.details[path], nil
}

func (s stubGit) DefaultBranch(_ context.Context, _, _ string) string { return "main" }

type stubErr struct{ msg string }

func (e stubErr) Error() string { return e.msg }

var errStubGit = stubErr{"stub git fails"}

type stubForge struct {
	hosts  []repos.Host
	access map[string]repos.Access
}

func (s stubForge) Status(_ context.Context) []repos.Host { return s.hosts }
func (s stubForge) Access(_ context.Context, top string) (repos.Access, bool) {
	a, ok := s.access[top]
	return a, ok
}

func TestRefreshSnapshot(t *testing.T) {
	t.Parallel()
	top := "/repos/a"
	r := repos.NewRefresher(repos.Config{
		Roots: []string{"/repos"},
		Git: stubGit{details: map[string]repos.Detail{
			top: {
				Top: top, Branch: "fleet/12", Head: "abc123", Dirty: true,
				Remotes: []repos.Remote{{Name: "origin", Host: "github.com", Path: "acme/repo"}},
			},
		}},
		Forge: stubForge{
			hosts:  []repos.Host{{Tool: "gh", Host: "github.com", User: "ann", SignedIn: true}},
			access: map[string]repos.Access{top: {Push: true, Permission: "ADMIN"}},
		},
		Discover: func([]string) ([]string, error) { return []string{top}, nil },
		Clock:    func() time.Time { return time.Unix(1000, 0) },
	})
	snap := &agentv1.Snapshot{}
	if err := r.Refresh(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.GetRepos()) != 1 {
		t.Fatalf("repos = %d, want 1", len(snap.GetRepos()))
	}
	rc := snap.GetRepos()[0]
	if rc.GetPath() != top || rc.GetBranch().GetBranch() != "fleet/12" || !rc.GetBranch().GetDirty() {
		t.Fatalf("checkout = %+v, want path/branch/dirty", rc)
	}
	if rc.GetRepo().GetHost() != "github.com" || rc.GetRepo().GetPath() != "acme/repo" {
		t.Fatalf("repo = %+v, want github.com/acme/repo", rc.GetRepo())
	}
	if rc.GetRemoteName() != "origin" || rc.GetDefaultBranch() != "main" {
		t.Fatalf("remote = %q default = %q, want origin/main", rc.GetRemoteName(), rc.GetDefaultBranch())
	}
	if len(snap.GetForgeCli()) != 1 {
		t.Fatalf("forge = %d, want 1", len(snap.GetForgeCli()))
	}
	fc := snap.GetForgeCli()[0]
	if !fc.GetSignedIn() || fc.GetUser() != "ann" {
		t.Fatalf("forge = %+v, want signed in ann", fc)
	}
	if len(fc.GetAccess()) != 1 || !fc.GetAccess()[0].GetPushAccess() {
		t.Fatalf("access = %+v, want push", fc.GetAccess())
	}
}

func TestRefreshKeepsLastOnGitError(t *testing.T) {
	t.Parallel()
	top := "/repos/a"
	git := stubGit{details: map[string]repos.Detail{
		top: {Top: top, Branch: "fleet/12", Head: "abc123"},
	}}
	r := repos.NewRefresher(repos.Config{
		Roots:    []string{"/repos"},
		Git:      git,
		Forge:    stubForge{},
		Discover: func([]string) ([]string, error) { return []string{top}, nil },
		Clock:    time.Now,
	})
	if err := r.Refresh(context.Background(), &agentv1.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	git.err = map[string]bool{top: true}
	snap := &agentv1.Snapshot{}
	if err := r.Refresh(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.GetRepos()) != 1 || snap.GetRepos()[0].GetBranch().GetBranch() != "fleet/12" {
		t.Fatalf("repos = %+v, want the kept reading", snap.GetRepos())
	}
}

func TestWorkdirMapping(t *testing.T) {
	t.Parallel()
	top := "/repos/a"
	r := repos.NewRefresher(repos.Config{
		Roots: []string{"/repos"},
		Git: stubGit{details: map[string]repos.Detail{
			top: {
				Top: top, Branch: "fleet/12", Head: "abc123",
				Remotes: []repos.Remote{{Name: "origin", Host: "github.com", Path: "acme/repo"}},
			},
		}},
		Forge:    stubForge{},
		Discover: func([]string) ([]string, error) { return []string{top}, nil },
		Clock:    time.Now,
	})
	if err := r.Refresh(context.Background(), &agentv1.Snapshot{}); err != nil {
		t.Fatal(err)
	}
	// A subfolder maps to its checkout.
	wd := r.Workdir(top + "/pkg/x")
	if wd == nil || wd.GetCheckoutPath() != top || wd.GetRepo().GetPath() != "acme/repo" {
		t.Fatalf("workdir = %+v, want the checkout", wd)
	}
	if wd.GetBranch() != "fleet/12" {
		t.Fatalf("branch = %q, want fleet/12", wd.GetBranch())
	}
	// Outside every checkout but under a root: path only, no repo.
	wd = r.Workdir("/repos/scratch")
	if wd == nil || wd.GetRepo() != nil {
		t.Fatalf("workdir = %+v, want path without repo", wd)
	}
	// Outside every root: nothing, so the path never leaves the machine.
	if wd := r.Workdir("/etc"); wd != nil {
		t.Fatalf("workdir = %+v, want nil outside the roots", wd)
	}
}
