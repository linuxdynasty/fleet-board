package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/pacing"
	"github.com/linuxdynasty/fleet-board/internal/hub/scheduler"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// snapshotItems collects the READY issue-shaped mirrors: the recomputed
// trust verdict, the claim re-check and the live-goal check per item,
// and the watched repo each item's project maps to (03c rule 1). An
// item whose project names no watched repo stays in the snapshot with
// no repo entry, so the planner explains it as repo_not_watched.
func (f *Fleet) snapshotItems(ctx context.Context, tx *sql.Tx, now time.Time, trust TrustFunc,
	snap *scheduler.Snapshot, items map[string]itemCtx, repoCapDefault int64,
) error {
	rows, err := statedb.ListWorkItemRowsTx(ctx, tx)
	if err != nil {
		return err
	}
	watched := map[string][]statedb.WatchedRepo{}
	loaded := map[string]bool{}
	for _, row := range rows {
		// The stored kind reads short since migration 33 rewrote the
		// older full-form rows; the match still tolerates either
		// spelling.
		if hubv1.WorkStage(row.Stage) != hubv1.WorkStage_WORK_STAGE_READY || workKindShort(row.Kind) != "ISSUE" {
			continue
		}
		number, err := strconv.ParseUint(row.ItemKey, 10, 64)
		if err != nil {
			continue
		}
		var item *integrationv1.WorkItem
		if parsed := (&integrationv1.WorkItem{}); protojson.Unmarshal([]byte(row.ItemJSON), parsed) == nil {
			item = parsed
		}
		verdict := trust(item, row.Integration, row.Project, now)
		st, err := statedb.GetWorkItemStateTx(ctx, tx, row.Key)
		if err != nil {
			return fmt.Errorf("scheduler: read work item state: %w", err)
		}
		_, live, err := statedb.LiveGoalForWorkItemTx(ctx, tx, row.Key)
		if err != nil {
			return fmt.Errorf("scheduler: check live goal: %w", err)
		}
		repoName, repoHost, repoPath := matchWatchedRepo(ctx, tx, watched, row.Integration, row.Project)
		if repoName != "" && !loaded[repoName] {
			loaded[repoName] = true
			if err := loadSnapshotRepo(ctx, tx, snap, repoName, repoHost, repoPath, repoCapDefault); err != nil {
				return err
			}
		}
		snap.Items = append(snap.Items, scheduler.Item{
			Key: row.Key, Integration: row.Integration, Repo: repoName,
			Project: row.Project, Number: number, CreateMs: row.CreateMs,
			Stage:   hubv1.WorkStage_WORK_STAGE_READY,
			Trusted: verdict.GetTrusted(), Denial: verdict.GetDenial(),
			Claimed: foreignClaim(st.ClaimsJSON), LiveGoal: live,
		})
		items[row.Key] = itemCtx{
			integration: row.Integration, project: row.Project, itemKey: row.ItemKey,
			number: number, repoHost: repoHost, repoPath: repoPath,
		}
	}
	return nil
}

// matchWatchedRepo maps an item's project to the watched repo of its
// integration whose path equals the project (the syncer polls per repo,
// so the project is the repo path). The first host in order wins; empty
// means no watched repo matches.
func matchWatchedRepo(ctx context.Context, tx *sql.Tx, watched map[string][]statedb.WatchedRepo,
	integration, project string,
) (name, host, path string) {
	repos, ok := watched[integration]
	if !ok {
		list, err := statedb.ListWatchedReposTx(ctx, tx, integration)
		if err != nil {
			return "", "", ""
		}
		watched[integration] = list
		repos = list
	}
	for _, r := range repos {
		if r.Path == project {
			return r.Host + "/" + r.Path, r.Host, r.Path
		}
	}
	return "", "", ""
}

// loadSnapshotRepo reads one watched repo's dispatch facts: enabled and
// cap from the spec (a row the hub holds but disabled is not watched),
// the per-repo auto-dispatch override, the live goal count for the cap
// and the branch pattern. An unreadable or missing spec leaves the repo
// out, so the planner explains its items as repo_not_watched.
func loadSnapshotRepo(ctx context.Context, tx *sql.Tx, snap *scheduler.Snapshot,
	name, host, path string, repoCapDefault int64,
) error {
	stored, err := statedb.ReadRepoTx(ctx, tx, host, path)
	if errors.Is(err, statedb.ErrRepoUnknown) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scheduler: read repo: %w", err)
	}
	var spec hubv1.RepoSpec
	if !parseRepoSpec(stored.Spec, &spec) {
		return nil
	}
	cap := repoCapDefault
	if spec.MaxParallelGoals != nil {
		cap = int64(spec.GetMaxParallelGoals())
	}
	n, err := statedb.CountRepoLiveGoalsTx(ctx, tx, name)
	if err != nil {
		return fmt.Errorf("scheduler: count repo goals: %w", err)
	}
	if snap.Repos == nil {
		snap.Repos = map[string]scheduler.Repo{}
	}
	snap.Repos[name] = scheduler.Repo{
		Enabled: spec.GetEnabled(), Cap: cap, AutoDispatch: spec.AutoDispatch,
		LiveGoals: n, BranchPattern: spec.GetBranchPattern(),
	}
	return nil
}

// parseRepoSpec decodes one repo spec: false means the row is corrupt,
// so the caller leaves the repo out (its items read repo_not_watched).
// The spec is hub written, so false means a bigger problem.
func parseRepoSpec(raw []byte, spec *hubv1.RepoSpec) bool {
	return protojson.Unmarshal(raw, spec) == nil
}

// snapshotAccounts paces every account from its latest stored reading
// at plan time (03b, 03c rule 2): the pacing state decides
// dispatchability, allowed_active caps the account, and the projected
// waste ranks it. No reading, a failed parse or a stale reading paces
// UNKNOWN or STALE, never a guess; overage billing is not persisted, so
// the probe verdicts own it (row 10). OpenCode on an Anthropic provider
// never auto-dispatches (03c rule 2).
func (f *Fleet) snapshotAccounts(ctx context.Context, tx *sql.Tx, now time.Time, snap *scheduler.Snapshot) error {
	rows, err := statedb.ListAccountRowsTx(ctx, tx)
	if err != nil {
		return err
	}
	active, err := countActiveGoals(ctx, tx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var spec hubv1.AccountSpec
		if err := protojson.Unmarshal(row.Spec, &spec); err != nil {
			f.tel.Logger.WarnContext(ctx, "dispatch.skipped",
				"reason", "account_not_dispatchable", "account", row.ID, "detail", "unreadable spec")
			continue
		}
		if strings.EqualFold(row.Harness, "opencode") && strings.EqualFold(row.Provider, "anthropic") {
			snap.Accounts = append(snap.Accounts, scheduler.Account{
				ID: row.ID, Harness: row.Harness,
				Pacing: hubv1.PacingState_PACING_STATE_EXCLUDED,
			})
			continue
		}
		reserve := uint32(10)
		if spec.ReservePercent != nil {
			reserve = spec.GetReservePercent()
		}
		usage, err := snapshotUsage(ctx, tx, row.ID)
		if err != nil {
			return err
		}
		res := pacing.Evaluate(now, pacing.Input{
			ReservePercent: reserve, Budget: spec.GetBudgetKind(), Mode: spec.GetMode(),
			//nolint:gosec // G115: active counts are non-negative tallies of stored rows
			ActiveGoals: uint32(active[row.ID]), Usage: usage,
		})
		snap.Accounts = append(snap.Accounts, scheduler.Account{
			ID: row.ID, Harness: row.Harness, Pacing: res.Pacing.GetState(),
			AllowedActive: int64(res.Pacing.GetAllowedActive()),
			ActiveGoals:   int64(active[row.ID]),
			Waste:         float64(res.Pacing.GetProjectedUnusedPercent()),
		})
	}
	return nil
}

// countActiveGoals counts ACTIVE-class live goals per account from
// their stored definitions (03l "Phase classes"): the store keeps no
// account column, so the scheduler parses the definitions in memory.
// Fleets hold tens of live goals.
func countActiveGoals(ctx context.Context, tx *sql.Tx) (map[string]int, error) {
	specs, err := statedb.ListActiveGoalSpecsTx(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("scheduler: list active goals: %w", err)
	}
	out := map[string]int{}
	for _, g := range specs {
		var def hubv1.GoalDefinition
		if err := protojson.Unmarshal([]byte(g.Spec), &def); err != nil {
			// A corrupt row counts nowhere (the commit's machine
			// caps still bound the pass); the definition is hub
			// written, so this means a bigger problem.
			continue
		}
		if id := def.GetAccount().GetValue(); id != "" {
			out[id]++
		}
	}
	return out, nil
}

// snapshotUsage builds the pacing input from the account's latest
// stored reading: the hub-adjusted read time, the parse outcome and the
// buckets. Found false means no reading yet, which paces UNKNOWN.
func snapshotUsage(ctx context.Context, tx *sql.Tx, accountID string) (*hubv1.AccountUsage, error) {
	stored, found, err := statedb.LatestUsageFullTx(ctx, tx, accountID)
	if err != nil {
		return nil, fmt.Errorf("scheduler: read usage: %w", err)
	}
	if !found {
		return nil, nil
	}
	buckets, err := parseBuckets(stored.BucketsJSON)
	if err != nil {
		return nil, fmt.Errorf("scheduler: parse buckets: %w", err)
	}
	return &hubv1.AccountUsage{
		ReadTime: timestamppb.New(time.UnixMilli(stored.ReadMs)),
		Parse:    usageParse(stored.Parse),
		Buckets:  buckets,
	}, nil
}

// usageParse maps the stored parse short name to the proto outcome;
// anything else reads FAILED, so a corrupt row blocks dispatch instead
// of passing a guess.
func usageParse(raw string) typev1.ParseOutcome {
	switch raw {
	case "OK":
		return typev1.ParseOutcome_PARSE_OUTCOME_OK
	case "PARTIAL":
		return typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL
	case "FAILED":
		return typev1.ParseOutcome_PARSE_OUTCOME_FAILED
	default:
		return typev1.ParseOutcome_PARSE_OUTCOME_FAILED
	}
}

// parseBuckets decodes the stored buckets array, one proto per element.
func parseBuckets(raw []byte) ([]*typev1.LimitBucket, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, err
	}
	var out []*typev1.LimitBucket
	for _, e := range elems {
		var b typev1.LimitBucket
		if err := protojson.Unmarshal(e, &b); err != nil {
			return nil, err
		}
		out = append(out, &b)
	}
	return out, nil
}

// snapshotMachines collects every machine's dispatch facts (03c rule 3):
// ONLINE undrained machines under both slot caps whose harness the
// account may launch, minus fresh unfit markers for the scope. The hub
// machine resolves by hostname against the snapshot facts when the
// owner prefers other machines first; with no (or an ambiguous) match
// every machine ranks the same and the preference stays off.
func (f *Fleet) snapshotMachines(ctx context.Context, tx *sql.Tx, now time.Time, preferNonHub bool, snap *scheduler.Snapshot) error {
	dispatch, err := statedb.ListMachineDispatchTx(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := statedb.ListMachineRowsTx(ctx, tx)
	if err != nil {
		return err
	}
	facts := map[string]string{}
	for _, r := range rows {
		facts[r.MachineID] = r.Facts
	}
	hubID := ""
	if preferNonHub {
		hubID = hubMachineID(facts, f.hubHostname)
	}
	for _, m := range dispatch {
		invs, err := statedb.ListMachineHarnessTx(ctx, tx, m.ID)
		if err != nil {
			return err
		}
		harnesses := map[string]bool{}
		for _, inv := range invs {
			if inv.LaunchAllowed {
				harnesses[inv.Harness] = true
			}
		}
		maxActive, maxTotal, err := machineSlotCaps(ctx, tx, m.ID)
		if err != nil {
			return err
		}
		var unfit []scheduler.UnfitMark
		for _, mark := range m.Unfit {
			unfit = append(unfit, scheduler.UnfitMark{
				Harness: mark.Harness, Repo: mark.Repo,
				Fresh: statedb.UnfitFresh(mark, now.UnixMilli()),
			})
		}
		snap.Machines = append(snap.Machines, scheduler.Machine{
			ID: m.ID, Hub: m.ID == hubID,
			Online: m.State == statedb.MachineStateOnline, Draining: m.Draining,
			Active: m.Active, Total: m.Total,
			MaxActive: maxActive, MaxTotal: maxTotal,
			Harnesses: harnesses, Unfit: unfit,
		})
	}
	return nil
}

// hubMachineID resolves the hub machine by hostname against the
// snapshot facts: the single machine whose reported hostname matches
// the daemon's. Empty hub hostname, no match or an ambiguous match
// resolves to no preference rather than the wrong machine.
func hubMachineID(facts map[string]string, hubHostname string) string {
	if hubHostname == "" {
		return ""
	}
	var matches []string
	for id, raw := range facts {
		var mf agentv1.MachineFacts
		if err := protojson.Unmarshal([]byte(raw), &mf); err != nil {
			continue
		}
		if h := mf.GetHostname(); h != "" && strings.EqualFold(h, hubHostname) {
			matches = append(matches, id)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

// machineSlotCaps reads one machine's slot caps: the machine settings
// layer wins, else zero, which the planner reads as the fleet defaults
// (dispatchMachine refuses zero the same way: a layer of zero unsets).
func machineSlotCaps(ctx context.Context, tx *sql.Tx, machineID string) (int64, int64, error) {
	raw, found, err := statedb.ReadSettingsLayerTx(ctx, tx, statedb.MachineScopeKey(machineID))
	if err != nil {
		return 0, 0, fmt.Errorf("scheduler: read machine settings: %w", err)
	}
	if !found {
		return 0, 0, nil
	}
	var lspec hubv1.SettingsSpec
	if err := protojson.Unmarshal([]byte(raw), &lspec); err != nil {
		return 0, 0, fmt.Errorf("scheduler: parse machine settings: %w", err)
	}
	return int64(lspec.GetValues().GetMaxActive()), int64(lspec.GetValues().GetMaxTotal()), nil
}
