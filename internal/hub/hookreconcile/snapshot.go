package hookreconcile

import (
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// SnapshotInput converts one agent snapshot into a Decide input: the
// machine harness entries, the checkout hook states and the registry
// for the live-goal guard. repos, expected, sent, now and resendAfter
// pass through from the caller (watched repos, hub versions, the resend
// throttle).
func SnapshotInput(snap *agentv1.Snapshot, repos []Repo, expected map[string]string,
	sent map[Key]time.Time, now time.Time, resendAfter time.Duration,
) Input {
	in := Input{Repos: repos, Expected: expected, Sent: sent, Now: now, ResendAfter: resendAfter}
	for _, hi := range snap.GetHarnesses() {
		in.Harnesses = append(in.Harnesses, Harness{
			Name: hi.GetHarness().GetName(), Profile: hi.GetProfile(),
			LaunchAllowed: hi.GetLaunchAllowed(), State: hookStateOf(hi.GetGoalHooks()),
		})
	}
	for _, rc := range snap.GetRepos() {
		c := Checkout{RepoHost: rc.GetRepo().GetHost(), RepoPath: rc.GetRepo().GetPath(), Hooks: map[HP]HookState{}}
		for _, gh := range rc.GetGoalHooks() {
			c.Hooks[HP{Harness: gh.GetHarness().GetName()}] = hookStateOf(gh.GetState())
		}
		in.Checkouts = append(in.Checkouts, c)
	}
	in.Live = LiveHarnesses(snap.GetGoalRegistry())
	return in
}

// hookStateOf reads one GoalHooksState. A nil state is not Present
// (missing); an empty version with no expected gate still decides on
// installed and verified alone.
func hookStateOf(state *typev1.GoalHooksState) HookState {
	if state == nil {
		return HookState{}
	}
	return HookState{
		Present: true, Installed: state.GetInstalled(), Verified: state.GetVerified(),
		Version: state.GetHookVersion(), RepoShipsOwn: state.GetRepoShipsOwn(),
	}
}
