package repos

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// forgeRefreshInterval is how often gh is asked again (H4, and the
// forge_cli_status event: the hourly re-check): sign-in and push access
// change slowly, and each answer costs an API call.
const forgeRefreshInterval = time.Hour

// Config wires a Refresher. Roots are allowed_repo_roots in order;
// RemoteFor names the policy remote of a root (repo_remote), "" for the
// default. Discover and Clock are seams for tests; nil Discover scans the
// real roots, nil Clock uses the wall clock.
type Config struct {
	Roots     []string
	RemoteFor func(root string) string
	Git       Git
	Forge     Forge
	Discover  func(roots []string) ([]string, error)
	Clock     func() time.Time
}

// Refresher keeps the checkout table (H1, H3) and forge state (H4) and
// fills the Snapshot's repos and forge_cli. A checkout git cannot read
// keeps its last reading; one discovery no longer lists is dropped. It is
// not safe for concurrent use; the snapshot hook calls it.
type Refresher struct {
	cfg      Config
	tops     []string
	details  map[string]Detail
	forge    []*agentv1.ForgeCliStatus
	forgedAt time.Time
}

// NewRefresher builds a Refresher; Config.Git and Config.Forge are required.
func NewRefresher(cfg Config) *Refresher {
	if cfg.Discover == nil {
		cfg.Discover = Discover
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &Refresher{cfg: cfg, details: map[string]Detail{}}
}

// SetRoots replaces the allowed roots (the policy watcher may change
// them); the next Refresh scans the new ones.
func (r *Refresher) SetRoots(roots []string) { r.cfg.Roots = roots }

// Refresh rescans the roots and fills snap's repos and forge_cli. Git
// failures keep the last reading; gh is re-asked at most every
// forgeRefreshInterval.
func (r *Refresher) Refresh(ctx context.Context, snap *agentv1.Snapshot) error {
	var tops []string
	rootOf := map[string]string{}
	for _, root := range r.cfg.Roots {
		found, err := r.cfg.Discover([]string{root})
		if err != nil {
			return err
		}
		for _, top := range found {
			if _, seen := rootOf[top]; !seen {
				rootOf[top] = root
				tops = append(tops, top)
			}
		}
	}
	now := r.cfg.Clock()
	next := map[string]Detail{}
	for _, top := range tops {
		d, err := r.cfg.Git.Inspect(ctx, top)
		if err != nil {
			if last, ok := r.details[top]; ok {
				next[top] = last // keep the last reading, never guess
			}
			continue
		}
		d.RemoteName = pickRemote(d, r.remoteFor(rootOf[top], top))
		next[top] = d
	}
	r.tops, r.details = tops, next
	if now.Sub(r.forgedAt) >= forgeRefreshInterval || r.forge == nil {
		r.forge = r.readForge(ctx, now)
		r.forgedAt = now
	}
	fillRepos(snap, r.tops, r.details, rootOf, r.remoteFor, r.cfg.Git, ctx, now)
	snap.ForgeCli = r.forge
	return nil
}

func (r *Refresher) remoteFor(root, top string) string {
	if r.cfg.RemoteFor != nil {
		if name := r.cfg.RemoteFor(root); name != "" {
			return name
		}
	}
	return "origin"
}

// readForge asks gh once per host and repo. Failures keep silence: gh
// already answered what it could, and absence reads as unknown.
func (r *Refresher) readForge(ctx context.Context, now time.Time) []*agentv1.ForgeCliStatus {
	hosts := r.cfg.Forge.Status(ctx)
	out := make([]*agentv1.ForgeCliStatus, 0, len(hosts))
	for _, h := range hosts {
		st := &agentv1.ForgeCliStatus{
			Tool: h.Tool, Host: h.Host, SignedIn: h.SignedIn, User: h.User,
			CheckedTime: timestamppb.New(now),
		}
		for _, top := range r.tops {
			d := r.details[top]
			if d.Top == "" || !repoOnHost(d, h.Host) {
				continue
			}
			a, ok := r.cfg.Forge.Access(ctx, top)
			if !ok {
				continue
			}
			st.Access = append(st.Access, &agentv1.RepoAccess{
				Repo: repoRefOf(d), PushAccess: a.Push,
				CheckedTime: timestamppb.New(now), ProviderPermission: a.Permission,
			})
		}
		out = append(out, st)
	}
	return out
}

func repoOnHost(d Detail, host string) bool {
	for _, rm := range d.Remotes {
		if rm.Host == host {
			return true
		}
	}
	return false
}

func repoRefOf(d Detail) *typev1.RepoRef {
	for _, rm := range d.Remotes {
		if rm.Name == d.RemoteName {
			return &typev1.RepoRef{Host: rm.Host, Path: rm.Path}
		}
	}
	return nil
}

// Detail returns the last reading of a checkout, false when discovery
// does not list it or git never read it.
func (r *Refresher) Detail(top string) (Detail, bool) {
	d, ok := r.details[top]
	return d, ok && d.Top != ""
}

// LookupRepo resolves a repo short name to its checkout top (row 20):
// the first checkout whose policy remote names host and path. False when
// no checkout names it, so the H2/F7/G1 commands refuse CHECKOUT_MISSING
// instead of guessing.
func (r *Refresher) LookupRepo(host, path string) (string, bool) {
	for _, top := range r.tops {
		d, ok := r.details[top]
		if !ok {
			continue
		}
		if ref := repoRefOf(d); ref != nil && ref.GetHost() == host && ref.GetPath() == path {
			return top, true
		}
	}
	return "", false
}

// Workdir fills the pane's workdir (02h "Reuse"): the pane path under an
// allowed root, with the containing checkout's repo, branch and dirt.
// Outside every root it is nil, so the path never leaves the machine;
// under a root but outside a checkout it carries the path alone.
func (r *Refresher) Workdir(path string) *typev1.PaneWorkdir {
	clean := filepath.Clean(path)
	rooted := false
	for _, root := range r.cfg.Roots {
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			rooted = true
			break
		}
	}
	if !rooted {
		return nil
	}
	wd := &typev1.PaneWorkdir{CurrentPath: clean}
	var best string
	for _, top := range r.tops {
		if clean == top || strings.HasPrefix(clean, top+string(filepath.Separator)) {
			if len(top) > len(best) {
				best = top
			}
		}
	}
	d, ok := r.details[best]
	if !ok || d.Top == "" {
		return wd
	}
	wd.CheckoutPath, wd.IsWorktree = d.Top, d.IsWorktree
	wd.Branch, wd.Dirty = d.Branch, d.Dirty
	if ref := repoRefOf(d); ref != nil {
		wd.Repo = ref
	}
	return wd
}

func fillRepos(snap *agentv1.Snapshot, tops []string, details map[string]Detail, rootOf map[string]string,
	remoteFor func(root, top string) string, git Git, ctx context.Context, now time.Time,
) {
	snap.Repos = nil
	for _, top := range tops {
		d := details[top]
		if d.Top == "" {
			continue
		}
		rc := &agentv1.RepoCheckout{
			Path: top, IsWorktree: d.IsWorktree, MainCheckoutPath: d.MainCheckout,
			RemoteName: d.RemoteName, DefaultBranch: git.DefaultBranch(ctx, top, d.RemoteName),
			Branch: &agentv1.BranchState{
				Path: top, Branch: d.Branch, HeadCommit: d.Head,
				Ahead: d.Ahead, Behind: d.Behind, Dirty: d.Dirty,
				UpstreamUnknown: d.UpstreamUnknown, ObservedTime: timestamppb.New(now),
			},
		}
		// The policy remote wins when the checkout has it, else origin;
		// with neither remote the repo stays unset and the checkout is
		// never a goal target (02h "Repo identity").
		for _, rm := range d.Remotes {
			rc.Remotes = append(rc.Remotes, &agentv1.GitRemote{
				Name: rm.Name, Repo: &typev1.RepoRef{Host: rm.Host, Path: rm.Path},
			})
		}
		if ref := repoRefOf(d); ref != nil {
			rc.Repo = ref
			rc.Branch.Repo = ref
		}
		snap.Repos = append(snap.Repos, rc)
	}
}

// pickRemote chooses the remote the repo is derived from: the policy
// remote when the checkout has it, else origin, else none (02h "Repo
// identity": with no remote the repo stays unset and the checkout is
// never a goal target).
func pickRemote(d Detail, preferred string) string {
	has := map[string]bool{}
	for _, rm := range d.Remotes {
		has[rm.Name] = true
	}
	switch {
	case preferred != "" && has[preferred]:
		return preferred
	case has["origin"]:
		return "origin"
	default:
		return ""
	}
}
