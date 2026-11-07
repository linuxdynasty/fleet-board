package scheduler

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// readyItem builds a READY, trusted, unclaimed issue-shaped item with no
// live goal: the dispatchable baseline every case varies.
func readyItem(key, repo string, number uint64, createMs int64) Item {
	return Item{
		Key: key, Integration: "github", Repo: repo, Project: repo,
		Number: number, CreateMs: createMs, Stage: hubv1.WorkStage_WORK_STAGE_READY,
		Trusted: true, Claimed: false, LiveGoal: false,
	}
}

func okAccount(id, harness string, waste float64) Account {
	return Account{
		ID: id, Harness: harness, Pacing: hubv1.PacingState_PACING_STATE_OK,
		AllowedActive: 2, ActiveGoals: 0, Waste: waste,
	}
}

func fitMachine(id string, hub bool) Machine {
	return Machine{
		ID: id, Hub: hub, Online: true, Draining: false,
		Active: 0, Total: 0, MaxActive: 2, MaxTotal: 4,
		Harnesses: map[string]bool{"claude": true}, Unfit: nil,
	}
}

func baseSnap() Snapshot {
	return Snapshot{
		Now:          time.UnixMilli(1_800_000_000_000).UTC(),
		AutoDispatch: true,
		Items: []Item{
			readyItem("github/ISSUE/acme/web/11", "acme/web", 11, 1_700_000_000_000),
			readyItem("github/ISSUE/acme/web/12", "acme/web", 12, 1_700_000_010_000),
		},
		Repos: map[string]Repo{
			"acme/web": {Enabled: true, Cap: 3, LiveGoals: 0, BranchPattern: "fleet/{work}"},
		},
		Accounts: []Account{okAccount("acc-claude", "claude", 12.5)},
		Machines: []Machine{fitMachine("m-worker", false)},
	}
}

func dispatchKeys(d Decision) []string {
	var out []string
	for _, x := range d.Dispatches {
		out = append(out, x.ItemKey+"@"+x.AccountID+"@"+x.MachineID)
	}
	return out
}

func TestPlan(t *testing.T) {
	t.Parallel()
	off := false
	on := true
	cases := []struct {
		name      string
		mutate    func(*Snapshot)
		want      []string
		wantSkips map[string]int64
		wantFleet []string
	}{
		{
			name: "ready oldest first",
			want: []string{
				"github/ISSUE/acme/web/11@acc-claude@m-worker",
			},
			// One pass dispatches one goal per machine: the second item
			// waits for the next pass (flood guard).
			wantSkips: map[string]int64{"flood_guard": 1},
		},
		{
			name: "untrusted item skipped",
			mutate: func(s *Snapshot) {
				s.Items[0].Trusted = false
				s.Items[0].Denial = hubv1.TrustDenial_TRUST_DENIAL_NOT_ALLOW_LISTED
			},
			want: []string{
				"github/ISSUE/acme/web/12@acc-claude@m-worker",
			},
			wantSkips: map[string]int64{"work_item_untrusted": 1},
		},
		{
			name: "claimed item skipped",
			mutate: func(s *Snapshot) {
				s.Items[0].Claimed = true
			},
			want: []string{
				"github/ISSUE/acme/web/12@acc-claude@m-worker",
			},
			wantSkips: map[string]int64{"work_item_claimed": 1},
		},
		{
			name: "live goal busy",
			mutate: func(s *Snapshot) {
				s.Items[0].LiveGoal = true
			},
			want: []string{
				"github/ISSUE/acme/web/12@acc-claude@m-worker",
			},
			wantSkips: map[string]int64{"work_item_busy": 1},
		},
		{
			name: "repo at cap holds both",
			mutate: func(s *Snapshot) {
				s.Repos["acme/web"] = Repo{Enabled: true, Cap: 1, LiveGoals: 1, BranchPattern: "fleet/{work}"}
			},
			want:      nil,
			wantSkips: map[string]int64{"repo_parallel_limit": 2},
		},
		{
			name: "in-pass counting holds the second",
			mutate: func(s *Snapshot) {
				s.Repos["acme/web"] = Repo{Enabled: true, Cap: 1, LiveGoals: 0, BranchPattern: "fleet/{work}"}
				s.Machines = append(s.Machines, fitMachine("m-extra", false))
				s.Accounts = append(s.Accounts, okAccount("acc-second", "claude", 1.5))
			},
			want: []string{
				"github/ISSUE/acme/web/11@acc-claude@m-extra",
			},
			wantSkips: map[string]int64{"repo_parallel_limit": 1},
		},
		{
			name: "unwatched repo skipped",
			mutate: func(s *Snapshot) {
				delete(s.Repos, "acme/web")
			},
			want:      nil,
			wantSkips: map[string]int64{"repo_not_watched": 2},
		},
		{
			name: "disabled repo skipped",
			mutate: func(s *Snapshot) {
				s.Repos["acme/web"] = Repo{Enabled: false, Cap: 3, BranchPattern: "fleet/{work}"}
			},
			want:      nil,
			wantSkips: map[string]int64{"repo_not_watched": 2},
		},
		{
			name: "reserve account excluded",
			mutate: func(s *Snapshot) {
				s.Accounts[0].Pacing = hubv1.PacingState_PACING_STATE_RESERVE
			},
			want:      nil,
			wantSkips: map[string]int64{"account_pacing_blocked": 2},
		},
		{
			name: "stale reading excluded",
			mutate: func(s *Snapshot) {
				s.Accounts[0].Pacing = hubv1.PacingState_PACING_STATE_STALE
			},
			want:      nil,
			wantSkips: map[string]int64{"account_usage_unknown": 2},
		},
		{
			name: "unknown account excluded",
			mutate: func(s *Snapshot) {
				s.Accounts[0].Pacing = hubv1.PacingState_PACING_STATE_UNKNOWN
			},
			want:      nil,
			wantSkips: map[string]int64{"account_usage_unknown": 2},
		},
		{
			name: "excluded account refused",
			mutate: func(s *Snapshot) {
				s.Accounts[0].Pacing = hubv1.PacingState_PACING_STATE_EXCLUDED
			},
			want:      nil,
			wantSkips: map[string]int64{"account_not_dispatchable": 2},
		},
		{
			name: "account at allowed active skipped",
			mutate: func(s *Snapshot) {
				s.Accounts[0].ActiveGoals = 2
			},
			want:      nil,
			wantSkips: map[string]int64{"account_pacing_blocked": 2},
		},
		{
			name: "most waste first",
			mutate: func(s *Snapshot) {
				s.Accounts = append(s.Accounts, okAccount("acc-flat", "claude", 30.0))
				s.Machines = append(s.Machines, fitMachine("m-second", false))
			},
			want: []string{
				"github/ISSUE/acme/web/11@acc-flat@m-second",
				"github/ISSUE/acme/web/12@acc-claude@m-worker",
			},
		},
		{
			name: "prefer non-hub",
			mutate: func(s *Snapshot) {
				s.Machines = append([]Machine{fitMachine("m-hub", true)}, s.Machines...)
				s.Items = s.Items[:1]
			},
			want: []string{
				"github/ISSUE/acme/web/11@acc-claude@m-worker",
			},
		},
		{
			name: "most free slots wins",
			mutate: func(s *Snapshot) {
				busy := fitMachine("m-busy", false)
				busy.Active = 1
				s.Machines = append([]Machine{busy}, s.Machines...)
				s.Items = s.Items[:1]
			},
			want: []string{
				"github/ISSUE/acme/web/11@acc-claude@m-worker",
			},
		},
		{
			name: "offline and draining machines skipped",
			mutate: func(s *Snapshot) {
				s.Machines[0].Online = false
				drained := fitMachine("m-drained", false)
				drained.Draining = true
				s.Machines = append(s.Machines, drained)
				s.Items = s.Items[:1]
			},
			want:      nil,
			wantSkips: map[string]int64{"no_fit_machine": 1},
		},
		{
			name: "machine at slot cap skipped",
			mutate: func(s *Snapshot) {
				s.Machines[0].Active = 2
				s.Items = s.Items[:1]
			},
			want:      nil,
			wantSkips: map[string]int64{"no_fit_machine": 1},
		},
		{
			name: "harness not launchable skipped",
			mutate: func(s *Snapshot) {
				s.Machines[0].Harnesses = map[string]bool{}
				s.Items = s.Items[:1]
			},
			want:      nil,
			wantSkips: map[string]int64{"no_fit_machine": 1},
		},
		{
			name: "fresh unfit marker skipped",
			mutate: func(s *Snapshot) {
				s.Machines[0].Unfit = []UnfitMark{{Harness: "claude", Repo: "acme/web", Fresh: true}}
				s.Items = s.Items[:1]
			},
			want:      nil,
			wantSkips: map[string]int64{"no_fit_machine": 1},
		},
		{
			name: "stale unfit marker ignored",
			mutate: func(s *Snapshot) {
				s.Machines[0].Unfit = []UnfitMark{{Harness: "claude", Repo: "acme/web", Fresh: false}}
				s.Items = s.Items[:1]
			},
			want: []string{
				"github/ISSUE/acme/web/11@acc-claude@m-worker",
			},
		},
		{
			name: "auto dispatch off dispatches nothing",
			mutate: func(s *Snapshot) {
				s.AutoDispatch = false
			},
			want:      nil,
			wantFleet: []string{"auto_dispatch_off"},
		},
		{
			name: "repo override turns auto dispatch on",
			mutate: func(s *Snapshot) {
				s.AutoDispatch = false
				r := s.Repos["acme/web"]
				r.AutoDispatch = &on
				s.Repos["acme/web"] = r
				s.Items = s.Items[:1]
			},
			want: []string{
				"github/ISSUE/acme/web/11@acc-claude@m-worker",
			},
		},
		{
			name: "repo opt out holds its items",
			mutate: func(s *Snapshot) {
				r := s.Repos["acme/web"]
				r.AutoDispatch = &off
				s.Repos["acme/web"] = r
			},
			want:      nil,
			wantSkips: map[string]int64{"auto_dispatch_off": 2},
		},
		{
			name: "empty fleet explains itself",
			mutate: func(s *Snapshot) {
				s.Items = nil
			},
			want:      nil,
			wantFleet: []string{"no_ready_items"},
		},
		{
			name: "ahead and behind still dispatch",
			mutate: func(s *Snapshot) {
				s.Accounts[0].Pacing = hubv1.PacingState_PACING_STATE_BEHIND
				s.Accounts[0].AllowedActive = 3
				s.Items = s.Items[:1]
			},
			want: []string{
				"github/ISSUE/acme/web/11@acc-claude@m-worker",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snap := baseSnap()
			if tc.mutate != nil {
				tc.mutate(&snap)
			}
			got := Plan(snap)
			keys := dispatchKeys(got)
			if len(keys) != len(tc.want) {
				t.Fatalf("dispatches = %v, want %v (skips %v fleet %v)",
					keys, tc.want, got.Skips, got.FleetBlocks)
			}
			for i := range keys {
				if keys[i] != tc.want[i] {
					t.Fatalf("dispatches = %v, want %v", keys, tc.want)
				}
			}
			if len(got.Skips) != len(tc.wantSkips) {
				t.Fatalf("skips = %v, want %v", got.Skips, tc.wantSkips)
			}
			for reason, n := range tc.wantSkips {
				if got.Skips[reason] != n {
					t.Fatalf("skips = %v, want %v", got.Skips, tc.wantSkips)
				}
			}
			if len(got.FleetBlocks) != len(tc.wantFleet) {
				t.Fatalf("fleet blocks = %v, want %v", got.FleetBlocks, tc.wantFleet)
			}
			for i := range got.FleetBlocks {
				if got.FleetBlocks[i] != tc.wantFleet[i] {
					t.Fatalf("fleet blocks = %v, want %v", got.FleetBlocks, tc.wantFleet)
				}
			}
		})
	}
}

func TestPlanBranchDisplay(t *testing.T) {
	t.Parallel()
	snap := baseSnap()
	snap.Items = snap.Items[:1]
	got := Plan(snap)
	if len(got.Dispatches) != 1 {
		t.Fatalf("dispatches = %v, want one", dispatchKeys(got))
	}
	d := got.Dispatches[0]
	if d.Display != "acme/web#11" {
		t.Errorf("display = %q, want %q", d.Display, "acme/web#11")
	}
	if d.BranchPattern != "fleet/{work}" {
		t.Errorf("branch pattern = %q, want fleet/{work}", d.BranchPattern)
	}
}

func TestGatePass(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_800_000_000_000).UTC()
	if !GatePass(now, time.Time{}, PassEvery) {
		t.Error("GatePass with no last pass = false, want true (first pass runs)")
	}
	if GatePass(now, now.Add(-29*time.Second), PassEvery) {
		t.Error("GatePass 29 s after last = true, want false (30 s cadence)")
	}
	if !GatePass(now, now.Add(-30*time.Second), PassEvery) {
		t.Error("GatePass 30 s after last = false, want true")
	}
	if !GatePass(now, now.Add(-time.Hour), PassEvery) {
		t.Error("GatePass an hour after last = false, want true (no catch-up burst, just one pass)")
	}
}

func TestEffectiveAutoDispatch(t *testing.T) {
	t.Parallel()
	on, off := true, false
	cases := []struct {
		name  string
		repo  *bool
		fleet bool
		want  bool
	}{
		{"fleet default off", nil, false, false},
		{"fleet default on", nil, true, true},
		{"repo opts out", &off, true, false},
		{"repo opts in", &on, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := EffectiveAutoDispatch(tc.repo, tc.fleet); got != tc.want {
				t.Errorf("EffectiveAutoDispatch = %v, want %v", got, tc.want)
			}
		})
	}
}
