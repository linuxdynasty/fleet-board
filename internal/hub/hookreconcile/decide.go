package hookreconcile

import (
	"sort"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// KnownHarnesses is the agent's default harness set (allowed_harnesses
// with no opinion): what an unset EnsureMachineHooks harness expands
// over. The agent refuses what it cannot run, so an uninstalled harness
// costs one refused command, never a guess.
func KnownHarnesses() []string {
	return []string{"claude", "codex", "muse", "cursor", "opencode"}
}

// Key is one (harness, profile, repo) hook target. The checkout path is
// deliberately not part of it: the hub addresses the repo short name and
// the agent joins it to its allowed roots.
type Key struct {
	Harness  string
	Profile  string
	RepoHost string
	RepoPath string
}

// HP is one harness profile inside a checkout's hook map.
type HP struct {
	Harness string
	Profile string
}

// HookState is the installed/verified/version report for one harness in
// one place: the machine-level HarnessInfo entry or a checkout's
// HarnessGoalHooks entry. Present false means never reported.
type HookState struct {
	Present      bool
	Installed    bool
	Verified     bool
	Version      string
	RepoShipsOwn bool
}

// Harness is one agent-reported machine harness entry.
type Harness struct {
	Name          string
	Profile       string
	LaunchAllowed bool
	State         HookState
}

// Checkout is one agent-reported checkout with its per-harness hook
// states (which carry repo_ships_own from EnsureGoalHooks step 1).
type Checkout struct {
	RepoHost string
	RepoPath string
	Hooks    map[HP]HookState
}

// Repo is one hub-watched repo with its hook convention.
type Repo struct {
	Host    string
	Path    string
	Enabled bool
	Mode    hubv1.RepoHooksMode
}

// Forced is one explicit trigger (the owner's button, a harness_changed
// event, a repo turned on or its mode changed, one GOAL_FILE_NOT_SEEN
// per machine and harness per day): it bypasses the state rules and the
// resend throttle, never the live-goal guard and never ESHU.
type Forced struct {
	Key    Key
	Reason string
}

// Input is one reconcile pass.
type Input struct {
	Harnesses []Harness
	Checkouts []Checkout
	Repos     []Repo
	// Expected is the hub's hook version per harness; "" means no
	// version gate for that harness (lane 2 owns the versions).
	Expected map[string]string
	// Live names harnesses with a goal live in one of their panes: no
	// hook command runs for them (03h). GoalRegistration has no
	// profile, so this is profile-insensitive by design.
	Live  map[string]bool
	Force []Forced
	// Sent holds recent sends for the resend throttle; Now is the hub
	// clock; ResendAfter <= 0 means always due.
	Sent        map[Key]time.Time
	Now         time.Time
	ResendAfter time.Duration
}

// Want is one EnsureGoalHooks to enqueue. Reason is missing,
// not_installed, unverified, behind or forced:<trigger>.
type Want struct {
	Key    Key
	Reason string
}

// Decide returns the EnsureGoalHooks wants for one pass, ordered by
// (harness, profile, repo host, repo path). ESHU repos only report;
// AUTO follows the checkout's repo_ships_own (unknown installs once to
// learn it); GENERIC installs regardless. A checkout whose repo is
// unknown or disabled, a harness the machine may not launch, and a
// harness with a live goal never want — even forced, because a forced
// install would fight the repo's own Stop guard or interrupt a goal.
func Decide(in Input) []Want {
	repos := map[[2]string]Repo{}
	for _, r := range in.Repos {
		if r.Enabled {
			repos[[2]string{r.Host, r.Path}] = r
		}
	}
	byHarness := map[HP]Harness{}
	for _, h := range in.Harnesses {
		byHarness[HP{Harness: h.Name, Profile: h.Profile}] = h
	}
	var wants []Want
	add := func(key Key, reason string) {
		wants = append(wants, Want{Key: key, Reason: reason})
	}
	for _, c := range in.Checkouts {
		repo, ok := repos[[2]string{c.RepoHost, c.RepoPath}]
		if !ok {
			continue
		}
		if repo.Mode == hubv1.RepoHooksMode_REPO_HOOKS_MODE_ESHU {
			continue
		}
		for hp, h := range byHarness {
			if !h.LaunchAllowed {
				continue
			}
			if in.Live[h.Name] {
				continue
			}
			key := Key{Harness: h.Name, Profile: h.Profile, RepoHost: c.RepoHost, RepoPath: c.RepoPath}
			state, ok := c.Hooks[hp]
			if !ok {
				state = h.State
			}
			if repo.Mode == hubv1.RepoHooksMode_REPO_HOOKS_MODE_AUTO && state.RepoShipsOwn {
				continue
			}
			switch reason, want := stateReason(state, in.Expected[h.Name]); {
			case !want:
			case throttled(in, key):
			default:
				add(key, reason)
			}
		}
	}
	for _, f := range in.Force {
		repo, ok := repos[[2]string{f.Key.RepoHost, f.Key.RepoPath}]
		if !ok {
			continue
		}
		if repo.Mode == hubv1.RepoHooksMode_REPO_HOOKS_MODE_ESHU {
			continue
		}
		h, ok := byHarness[HP{Harness: f.Key.Harness, Profile: f.Key.Profile}]
		if !ok || !h.LaunchAllowed {
			continue
		}
		if in.Live[h.Name] {
			continue
		}
		state := h.State
		if s, ok := checkoutHooks(in.Checkouts, f.Key); ok {
			state = s
		}
		if repo.Mode == hubv1.RepoHooksMode_REPO_HOOKS_MODE_AUTO && state.RepoShipsOwn {
			continue
		}
		add(f.Key, "forced:"+f.Reason)
	}
	sort.Slice(wants, func(i, j int) bool {
		a, b := wants[i].Key, wants[j].Key
		if a.Harness != b.Harness {
			return a.Harness < b.Harness
		}
		if a.Profile != b.Profile {
			return a.Profile < b.Profile
		}
		if a.RepoHost != b.RepoHost {
			return a.RepoHost < b.RepoHost
		}
		return a.RepoPath < b.RepoPath
	})
	return wants
}

// stateReason applies the state rules: missing, not installed,
// unverified, or behind the expected version.
func stateReason(state HookState, expected string) (string, bool) {
	switch {
	case !state.Present:
		return "missing", true
	case !state.Installed:
		return "not_installed", true
	case !state.Verified:
		return "unverified", true
	case expected != "" && state.Version != expected:
		return "behind", true
	default:
		return "", false
	}
}

// throttled reports a recent matching send inside the resend window.
func throttled(in Input, key Key) bool {
	if in.ResendAfter <= 0 {
		return false
	}
	sent, ok := in.Sent[key]
	if !ok || in.Now.IsZero() {
		return false
	}
	return in.Now.Sub(sent) < in.ResendAfter
}

// checkoutHooks finds the checkout-level state for a forced key.
func checkoutHooks(checkouts []Checkout, key Key) (HookState, bool) {
	for _, c := range checkouts {
		if c.RepoHost != key.RepoHost || c.RepoPath != key.RepoPath {
			continue
		}
		state, ok := c.Hooks[HP{Harness: key.Harness, Profile: key.Profile}]
		return state, ok
	}
	return HookState{}, false
}

// LiveHarnesses derives the live-goal guard from a snapshot's goal
// registry: every registered harness counts (a stale entry holds the
// guard until the hub's registry reconcile releases it, row 22).
func LiveHarnesses(reg []*agentv1.GoalRegistration) map[string]bool {
	live := map[string]bool{}
	for _, entry := range reg {
		if name := entry.GetHarness().GetName(); name != "" {
			live[name] = true
		}
	}
	return live
}

// CommandFor builds the EnsureGoalHooks command for one want: the repo
// short name (never a machine path) plus the hub's expected version.
func CommandFor(w Want, expected string) *agentv1.Command {
	return &agentv1.Command{Kind: &agentv1.Command_EnsureGoalHooks{
		EnsureGoalHooks: &agentv1.EnsureGoalHooks{
			Harness:         &typev1.Harness{Name: w.Key.Harness},
			Workdir:         &typev1.Workdir{Repo: &typev1.RepoRef{Host: w.Key.RepoHost, Path: w.Key.RepoPath}},
			ExpectedVersion: expected,
			Profile:         w.Key.Profile,
		},
	}}
}
