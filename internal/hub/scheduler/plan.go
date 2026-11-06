package scheduler

import (
	"sort"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// PassEvery is the scheduler cadence (defaults.md, 03c): at most one
// auto-dispatch pass per 30 s, plus coalesced wakeups the runner owns.
const PassEvery = 30 * time.Second

// Slot and cap defaults (defaults.md, 03c planning rules): used when the
// snapshot carries no explicit layer value (zero means unset, never "no
// slots": zero slots would run nothing, so the supported off switch is
// draining the machine).
const (
	// DefaultRepoCap is HubSettings.max_parallel_goals_per_repo unset.
	DefaultRepoCap = 3
	// DefaultMaxActive and DefaultMaxTotal are the machine slot caps.
	DefaultMaxActive = 2
	DefaultMaxTotal  = 4
)

// Skip reasons are the lower-cased short names of the 540-range
// ReasonCodes (03c "Telemetry"): the same words the runner counts in
// fleet_hub_scheduler_skips_total{reason} and logs in dispatch.skipped.
const (
	SkipNoReadyItems           = "no_ready_items"
	SkipAutoDispatchOff        = "auto_dispatch_off"
	SkipWorkItemUntrusted      = "work_item_untrusted" //nolint:gosec // G101: a skip-reason label, not a credential
	SkipWorkItemClaimed        = "work_item_claimed"
	SkipWorkItemBusy           = "work_item_busy"
	SkipRepoNotWatched         = "repo_not_watched"
	SkipRepoParallelLimit      = "repo_parallel_limit"
	SkipAccountPacingBlocked   = "account_pacing_blocked"
	SkipAccountUsageUnknown    = "account_usage_unknown"
	SkipAccountNotDispatchable = "account_not_dispatchable"
	SkipNoFitMachine           = "no_fit_machine"
	SkipFloodGuard             = "flood_guard"
	// The rest name commit-time refusals: the decision checks and the
	// busy races copy their lower-cased ReasonCode into the skip
	// counter, so each needs its constant here.
	SkipSlotsFull                 = "slots_full"
	SkipBranchBusy                = "branch_busy"
	SkipConsentEmpty              = "consent_empty"
	SkipMachineUnfit              = "machine_unfit"
	SkipDraining                  = "draining"
	SkipMachineOffline            = "machine_offline"
	SkipHarnessVersionUnsupported = "harness_version_unsupported"
	SkipIntegrationLag            = "integration_lag"
	SkipIntegrationDegraded       = "integration_degraded"
	SkipRoutineNeedsSpareQuota    = "routine_needs_spare_quota"
	SkipUnknownCheck              = "unknown_check"
)

// Item is one candidate work item. Stage, trust and claim state are what
// the runner recomputed from the mirror at plan time (03c rule 1); the
// planner only selects on them. Number is the GitHub issue number: the
// planner dispatches trusted issues (v1 DoD 7), and the 0.18 default goal
// text is issue-shaped (03c).
type Item struct {
	Key, Integration, Repo, Project string
	Number                          uint64
	CreateMs                        int64
	Stage                           hubv1.WorkStage
	Trusted                         bool
	Denial                          hubv1.TrustDenial
	Claimed                         bool
	LiveGoal                        bool
}

// Repo is one watched repo's dispatch facts.
type Repo struct {
	Enabled bool
	// Cap is the parallel-goal cap; <= 0 reads DefaultRepoCap.
	Cap int64
	// AutoDispatch overrides the fleet default when set; nil inherits.
	AutoDispatch *bool
	// LiveGoals counts live goals on the repo at snapshot time.
	LiveGoals int64
	// BranchPattern renders the goal branch; empty reads fleet/{work}.
	BranchPattern string
}

// Account is one account's dispatch facts. Pacing, AllowedActive and
// Waste are the pacing verdict the runner evaluated from the latest
// reading at plan time (03b, pacing.Evaluate); ActiveGoals counts the
// account's ACTIVE-class goals (an active slot each, 03l).
type Account struct {
	ID, Harness                string
	Pacing                     hubv1.PacingState
	AllowedActive, ActiveGoals int64
	// Waste is projected_unused_percent: most waste dispatches first.
	Waste float64
}

// UnfitMark is one durable unfit marker (03h): Fresh means the marker
// still holds the scope (past its re-check the agent's preflight
// decides instead). The runner copies statedb's freshness verdict here.
type UnfitMark struct {
	Harness, Repo string
	Fresh         bool
}

// covers reports whether the marker holds a (harness, repo) dispatch: an
// empty field covers every value of it (03h "Start failures").
func (m UnfitMark) covers(harness, repo string) bool {
	if m.Harness != "" && m.Harness != harness {
		return false
	}
	return m.Repo == "" || m.Repo == repo
}

// Machine is one machine's dispatch facts.
type Machine struct {
	ID               string
	Hub              bool
	Online, Draining bool
	Active, Total    int64
	// MaxActive and MaxTotal are the slot caps; <= 0 reads the defaults.
	MaxActive, MaxTotal int64
	// Harnesses names the launch-allowed harnesses on the machine.
	Harnesses map[string]bool
	Unfit     []UnfitMark
}

// Snapshot is everything one planner pass reads. The caller builds it
// from one consistent store read plus the recomputed trust and pacing
// verdicts; Plan itself touches nothing outside it.
type Snapshot struct {
	Now time.Time
	// AutoDispatch is the fleet default (HubSettings.auto_dispatch,
	// false); a repo's AutoDispatch overrides it per repo.
	AutoDispatch bool
	Items        []Item
	Repos        map[string]Repo
	Accounts     []Account
	Machines     []Machine
}

// Dispatch is one planned goal: the item, the ranked account and the fit
// machine. Display renders the window and branch slugs ("acme/web#11",
// 02k); the branch itself is minted at commit with the goal id, and the
// commit transaction re-checks BRANCH_BUSY there.
type Dispatch struct {
	ItemKey, Integration, Repo string
	Display, BranchPattern     string
	AccountID, Harness         string
	MachineID                  string
}

// Decision is one pass: the dispatches in item order, the per-reason
// skip counts (03c telemetry labels) and the fleet-wide blocks. Skips
// counts every item held back; FleetBlocks names why a pass dispatched
// nothing fleet-wide (AUTO_DISPATCH_OFF, NO_READY_ITEMS: explanations
// only, never preflight checks, 03h).
type Decision struct {
	Dispatches  []Dispatch
	Skips       map[string]int64
	FleetBlocks []string
}

// EffectiveAutoDispatch resolves the per-repo override over the fleet
// default (repo.proto, settings.proto): unset inherits.
func EffectiveAutoDispatch(repoAuto *bool, fleetDefault bool) bool {
	if repoAuto != nil {
		return *repoAuto
	}
	return fleetDefault
}

// GatePass reports whether a pass may run now: at most one pass per
// interval (03c rule 4). The first pass after a start (zero last) runs:
// after a hub outage the pass still waits out its normal cadence, so
// dispatch resumes at the normal pace with no catch-up burst.
func GatePass(now, last time.Time, every time.Duration) bool {
	if last.IsZero() {
		return true
	}
	return !now.Before(last.Add(every))
}

// Plan runs one scheduler pass over the snapshot (03c planning rules).
func Plan(snap Snapshot) Decision {
	out := Decision{Skips: map[string]int64{}}
	ready := readyItems(snap.Items)
	if len(ready) == 0 {
		out.FleetBlocks = []string{SkipNoReadyItems}
		return out
	}
	fleetOn := snap.AutoDispatch
	for _, repo := range snap.Repos {
		if EffectiveAutoDispatch(repo.AutoDispatch, snap.AutoDispatch) {
			fleetOn = true
			break
		}
	}
	if !fleetOn {
		out.FleetBlocks = []string{SkipAutoDispatchOff}
		return out
	}
	livePerRepo := map[string]int64{}
	for name, repo := range snap.Repos {
		livePerRepo[name] = repo.LiveGoals
	}
	ranked := rankAccounts(snap.Accounts)
	usedAccounts := map[string]bool{}
	usedMachines := map[string]bool{}
	for _, item := range ready {
		repo, watched := snap.Repos[item.Repo]
		if !watched || !repo.Enabled {
			out.Skips[SkipRepoNotWatched]++
			continue
		}
		if !EffectiveAutoDispatch(repo.AutoDispatch, snap.AutoDispatch) {
			out.Skips[SkipAutoDispatchOff]++
			continue
		}
		if !item.Trusted {
			out.Skips[SkipWorkItemUntrusted]++
			continue
		}
		if item.Claimed {
			out.Skips[SkipWorkItemClaimed]++
			continue
		}
		if item.LiveGoal {
			out.Skips[SkipWorkItemBusy]++
			continue
		}
		cap := repo.Cap
		if cap <= 0 {
			cap = DefaultRepoCap
		}
		if livePerRepo[item.Repo] >= cap {
			out.Skips[SkipRepoParallelLimit]++
			continue
		}
		fits, reason := fitPairs(item, repo, ranked, snap.Machines)
		if reason != "" {
			out.Skips[reason]++
			continue
		}
		placed := false
		for _, fit := range fits {
			if usedAccounts[fit.AccountID] || usedMachines[fit.MachineID] {
				continue
			}
			usedAccounts[fit.AccountID] = true
			usedMachines[fit.MachineID] = true
			livePerRepo[item.Repo]++
			out.Dispatches = append(out.Dispatches, fit)
			placed = true
			break
		}
		if !placed {
			// Every fit pair spent its account or machine earlier
			// this pass; the item waits for the next pass (03c rule 4).
			out.Skips[SkipFloodGuard]++
		}
	}
	return out
}

// readyItems keeps stage READY in dispatch order: oldest hub create_time
// first, ties by key ("next up" is the first, 03c rule 1).
func readyItems(items []Item) []Item {
	var out []Item
	for _, item := range items {
		if item.Stage == hubv1.WorkStage_WORK_STAGE_READY {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreateMs != out[j].CreateMs {
			return out[i].CreateMs < out[j].CreateMs
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// rankAccounts orders accounts by waste risk (03b "rank"): most unused
// quota relative to time left first, ties by id.
func rankAccounts(accounts []Account) []Account {
	out := append([]Account(nil), accounts...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Waste != out[j].Waste {
			return out[i].Waste > out[j].Waste
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// dispatchable reports whether the account takes a goal this pass (03c
// rule 2): a dispatchable pacing state with active goals below the
// allowed count. The skip reason names which half refused.
func dispatchable(a Account) (bool, string) {
	switch a.Pacing {
	case hubv1.PacingState_PACING_STATE_OK,
		hubv1.PacingState_PACING_STATE_BEHIND,
		hubv1.PacingState_PACING_STATE_AHEAD:
		if a.ActiveGoals >= a.AllowedActive {
			return false, SkipAccountPacingBlocked
		}
		return true, ""
	case hubv1.PacingState_PACING_STATE_RESERVE,
		hubv1.PacingState_PACING_STATE_SHORT_LIMIT:
		return false, SkipAccountPacingBlocked
	case hubv1.PacingState_PACING_STATE_UNKNOWN,
		hubv1.PacingState_PACING_STATE_STALE:
		return false, SkipAccountUsageUnknown
	default:
		return false, SkipAccountNotDispatchable
	}
}

// fitPairs lists every (account, machine) fit for the item in
// preference order: accounts waste-first, machines non-hub then most
// free active slots. The second return names the skip when nothing fits:
// the pacing reason when no account could take a goal, else
// NO_FIT_MACHINE.
func fitPairs(item Item, repo Repo, ranked []Account, machines []Machine) ([]Dispatch, string) {
	pattern := repo.BranchPattern
	if pattern == "" {
		pattern = "fleet/{work}"
	}
	var out []Dispatch
	accountReason := ""
	for _, account := range ranked {
		ok, reason := dispatchable(account)
		if !ok {
			if accountReason == "" {
				accountReason = reason
			}
			continue
		}
		for _, m := range rankMachines(item, account, machines) {
			out = append(out, Dispatch{
				ItemKey: item.Key, Integration: item.Integration, Repo: item.Repo,
				Display:       item.Project + "#" + itemKeyNum(item),
				BranchPattern: pattern,
				AccountID:     account.ID, Harness: account.Harness,
				MachineID: m.ID,
			})
		}
	}
	if len(out) > 0 {
		return out, ""
	}
	if allUndispatchable(ranked) {
		// No account could take a goal, so the item's skip names
		// the pacing reason rather than the machine fit.
		if accountReason == "" {
			accountReason = SkipAccountNotDispatchable
		}
		return nil, accountReason
	}
	// Some account could run, but no machine fits any of them.
	return nil, SkipNoFitMachine
}

// allUndispatchable reports whether no account could take a goal, so the
// item's skip names the pacing reason rather than the machine fit.
func allUndispatchable(ranked []Account) bool {
	for _, account := range ranked {
		if ok, _ := dispatchable(account); ok {
			return false
		}
	}
	return true
}

// itemKeyNum renders the issue number for the display slug.
func itemKeyNum(item Item) string {
	if item.Number == 0 {
		return item.Key
	}
	digits := []byte{}
	n := item.Number
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// rankMachines lists the fit machines for the item on the account's
// harness (03c rule 3) in preference order: ONLINE, not draining, no
// fresh unfit marker for the scope, harness launch-allowed, under both
// slot caps; non-hub first, then most free active slots, ties by id.
func rankMachines(item Item, account Account, machines []Machine) []Machine {
	var out []Machine
	for _, m := range machines {
		if !m.Online || m.Draining {
			continue
		}
		if !m.Harnesses[account.Harness] {
			continue
		}
		blocked := false
		for _, mark := range m.Unfit {
			if mark.Fresh && mark.covers(account.Harness, item.Repo) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		maxActive, maxTotal := m.MaxActive, m.MaxTotal
		if maxActive <= 0 {
			maxActive = DefaultMaxActive
		}
		if maxTotal <= 0 {
			maxTotal = DefaultMaxTotal
		}
		if m.Active >= maxActive || m.Total >= maxTotal {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hub != out[j].Hub {
			return !out[i].Hub
		}
		freeI, freeJ := slotFree(out[i]), slotFree(out[j])
		if freeI != freeJ {
			return freeI > freeJ
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// slotFree counts the machine's free active slots under its cap.
func slotFree(m Machine) int64 {
	maxActive := m.MaxActive
	if maxActive <= 0 {
		maxActive = DefaultMaxActive
	}
	return maxActive - m.Active
}
