package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// dispatch_facts.go: the tx-scoped reads behind one manual dispatch (plan
// row 22). PreflightGoal and StartGoal gather every hub-check fact inside
// the decision's writer transaction, so the checks and the goal row commit
// together or not at all (03f). The pure check table lives in the goals
// package; this file only reads.

// activePhaseList is the ACTIVE class of 03l (uses quota: an active slot
// and a total slot). Everything live but not active is HELD (a total slot
// only).
const activePhaseList = `'GOAL_PHASE_STARTING', 'GOAL_PHASE_START_UNKNOWN', 'GOAL_PHASE_RUNNING', 'GOAL_PHASE_STOPPED'`

// livePhaseList is the LIVE set (ACTIVE union HELD) for counting queries.
// The partial unique indexes in goalsDispatchSQL name the same set; the
// cross-check in the goals package tests them together.
const livePhaseList = activePhaseList + `, 'GOAL_PHASE_WAITING', 'GOAL_PHASE_BLOCKED_OWNER', 'GOAL_PHASE_STUCK',
	'GOAL_PHASE_COOLDOWN', 'GOAL_PHASE_PAUSED', 'GOAL_PHASE_LIMIT_WAIT', 'GOAL_PHASE_VERIFYING',
	'GOAL_PHASE_DISAGREE', 'GOAL_PHASE_PANE_LOST'`

// ReadRepoTx reads one watched repo inside the caller's transaction
// (ErrRepoUnknown when missing).
func ReadRepoTx(ctx context.Context, tx *sql.Tx, host, path string) (RepoStored, error) {
	return readRepo(ctx, tx, host, path)
}

// ReadAccountTx reads one account inside the caller's transaction
// (ErrAccountUnknown when missing).
func ReadAccountTx(ctx context.Context, tx *sql.Tx, id string) (AccountStored, error) {
	return readAccount(ctx, tx, id)
}

// MachineDispatch is what one dispatch decision reads about a machine:
// its connection state, whether it drains, and its live goal load by
// phase class (03l "Phase classes": ACTIVE goals hold an active and a
// total slot, HELD goals a total slot only).
type MachineDispatch struct {
	ID             string
	State          string
	Draining       bool
	Active, Total  int64
	Unfit          []UnfitMark
	UnfitCheckedMs int64
}

// ReadMachineDispatchTx reads one machine's dispatch facts in the
// caller's transaction (ErrMachineUnknown when missing). The unfit
// markers come with it, so the caller matches the dispatch scope without
// a second round trip.
func ReadMachineDispatchTx(ctx context.Context, tx *sql.Tx, id string) (MachineDispatch, error) {
	var m MachineDispatch
	var state string
	var drained int64
	err := tx.QueryRowContext(ctx, `SELECT machine_id, state, drained FROM machines WHERE machine_id = ?`,
		id).Scan(&m.ID, &state, &drained)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineDispatch{}, ErrMachineUnknown
	}
	if err != nil {
		return MachineDispatch{}, fmt.Errorf("statedb: read machine dispatch: %w", err)
	}
	m.State = state
	m.Draining = drained != 0
	if err := countMachineGoals(ctx, tx, id, &m); err != nil {
		return MachineDispatch{}, err
	}
	marks, err := ListUnfit(ctx, tx, id)
	if err != nil {
		return MachineDispatch{}, err
	}
	m.Unfit = marks
	return m, nil
}

// ListMachineDispatchTx reads every machine's dispatch facts in key
// order, for the row-22 first-fit placement (row 23 ranks).
func ListMachineDispatchTx(ctx context.Context, tx *sql.Tx) ([]MachineDispatch, error) {
	rows, err := tx.QueryContext(ctx, `SELECT machine_id, state, drained FROM machines ORDER BY machine_id`)
	if err != nil {
		return nil, fmt.Errorf("statedb: list machine dispatch: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []MachineDispatch
	for rows.Next() {
		var m MachineDispatch
		var drained int64
		if err := rows.Scan(&m.ID, &m.State, &drained); err != nil {
			return nil, fmt.Errorf("statedb: scan machine dispatch: %w", err)
		}
		m.Draining = drained != 0
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := countMachineGoals(ctx, tx, out[i].ID, &out[i]); err != nil {
			return nil, err
		}
		marks, err := ListUnfit(ctx, tx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Unfit = marks
	}
	return out, nil
}

func countMachineGoals(ctx context.Context, tx *sql.Tx, id string, m *MachineDispatch) error {
	err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN phase IN (`+activePhaseList+`) THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN phase IN (`+livePhaseList+`) THEN 1 ELSE 0 END), 0)
		FROM goals WHERE machine_id = ?`, id).Scan(&m.Active, &m.Total)
	if err != nil {
		return fmt.Errorf("statedb: count machine goals: %w", err)
	}
	return nil
}

// CountRepoLiveGoalsTx counts the live goals on one repo (host/path,
// the watched-repo identity) for the per-repo parallel cap.
func CountRepoLiveGoalsTx(ctx context.Context, tx *sql.Tx, repo string) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM goals WHERE repo = ? AND phase IN (`+livePhaseList+`)`,
		repo).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("statedb: count repo goals: %w", err)
	}
	return n, nil
}

// LiveGoalForWorkItemTx reads the live goal holding a work item, found
// false when none does. The caller refuses WORK_ITEM_BUSY on found.
func LiveGoalForWorkItemTx(ctx context.Context, tx *sql.Tx, workItem string) (GoalRow, bool, error) {
	var g GoalRow
	var end sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT `+goalColumns+` FROM goals WHERE work_item = ? AND phase IN (`+
		livePhaseList+`)`, workItem).Scan(&g.ID, &g.Origin, &g.MachineID, &g.Spec, &g.Status, &g.Phase, &g.Reason,
		&g.StartMs, &end, &g.ObservedMs, &g.HubSeq, &g.Version, &g.Attempt, &g.PreviousGoal,
		&g.WorkItem, &g.Repo, &g.Branch, &g.StartError)
	if errors.Is(err, sql.ErrNoRows) {
		return GoalRow{}, false, nil
	}
	if err != nil {
		return GoalRow{}, false, fmt.Errorf("statedb: read live goal: %w", err)
	}
	if end.Valid {
		g.EndMs = &end.Int64
	}
	return g, true, nil
}

// LiveGoalForBranchTx reads the live goal working on a repo and branch,
// found false when none does. The caller refuses BRANCH_BUSY on found.
func LiveGoalForBranchTx(ctx context.Context, tx *sql.Tx, repo, branch string) (GoalRow, bool, error) {
	var g GoalRow
	var end sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT `+goalColumns+` FROM goals WHERE repo = ? AND branch = ? AND phase IN (`+
		livePhaseList+`)`, repo, branch).Scan(&g.ID, &g.Origin, &g.MachineID, &g.Spec, &g.Status, &g.Phase,
		&g.Reason, &g.StartMs, &end, &g.ObservedMs, &g.HubSeq, &g.Version, &g.Attempt, &g.PreviousGoal,
		&g.WorkItem, &g.Repo, &g.Branch, &g.StartError)
	if errors.Is(err, sql.ErrNoRows) {
		return GoalRow{}, false, nil
	}
	if err != nil {
		return GoalRow{}, false, fmt.Errorf("statedb: read branch goal: %w", err)
	}
	if end.Valid {
		g.EndMs = &end.Int64
	}
	return g, true, nil
}

// ReadFleetHubSpecTx reads the fleet settings layer's hub section in the
// caller's transaction, empty when the owner never wrote one. The caller
// parses the HubSettings proto for default_consent, the repo cap and
// approval_timeout.
func ReadFleetHubSpecTx(ctx context.Context, tx *sql.Tx) (string, error) {
	var raw sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT spec_json FROM settings_layers WHERE scope = ?`,
		FleetScope).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("statedb: read fleet settings: %w", err)
	}
	return raw.String, nil
}

// LatestUsageTx reads the newest usage reading of one account: the read
// time and the parse state (UNKNOWN or STALE means the scheduler would
// not dispatch on it). Found false when the account has no reading.
func LatestUsageTx(ctx context.Context, tx *sql.Tx, accountID string) (readMs int64, parse string, found bool, err error) {
	err = tx.QueryRowContext(ctx, `SELECT read_ms, parse FROM usage_readings
		WHERE account_id = ? ORDER BY received_ms DESC, read_ms DESC LIMIT 1`, accountID).Scan(&readMs, &parse)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("statedb: read usage: %w", err)
	}
	return readMs, parse, true, nil
}

// ReadChangeRequestJSONTx reads one change-request mirror's payload in
// the caller's transaction (ErrChangeRequestUnknown when missing). The
// verifier parses the ChangeRequest for merge state, head, checks and
// review decision.
func ReadChangeRequestJSONTx(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT cr_json FROM change_requests WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrChangeRequestUnknown
	}
	if err != nil {
		return "", fmt.Errorf("statedb: read change request: %w", err)
	}
	return raw, nil
}

// ReadIntegrationStatusTx reads one integration's stored status in the
// caller's transaction.
func ReadIntegrationStatusTx(ctx context.Context, tx *sql.Tx, id string) (status string, err error) {
	st, err := readIntegrationStatus(ctx, tx, id)
	if err != nil {
		return "", err
	}
	return string(st.raw), nil
}

// ReadSettingsLayerTx reads one settings layer's spec in the caller's
// transaction, found false when the owner never wrote it.
func ReadSettingsLayerTx(ctx context.Context, tx *sql.Tx, scope string) (string, bool, error) {
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT spec_json FROM settings_layers WHERE scope = ?`, scope).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("statedb: read settings layer: %w", err)
	}
	return raw, true, nil
}

// MachineStateNames names the stored machine states for dispatch
// decisions (machinestate.go owns the transitions).
const (
	MachineStateOnline  = "ONLINE"
	MachineStateStale   = "STALE"
	MachineStateOffline = "OFFLINE"
)

// UnfitFresh reports whether an unfit marker still holds a dispatch: a
// marker past its re-check time is stale, and the agent's preflight
// decides instead (03h "re-check").
func UnfitFresh(m UnfitMark, nowMs int64) bool {
	return nowMs < m.NextCheckMs
}

// UnfitCovers reports whether a marker covers a dispatch scope: an empty
// harness or repo covers every value of it (TMUX_TOO_OLD marks every
// harness; a repo marker covers only that repo).
func UnfitCovers(m UnfitMark, harness, repo string) bool {
	if m.Harness != "" && !strings.EqualFold(m.Harness, harness) {
		return false
	}
	if m.Repo != "" && m.Repo != repo {
		return false
	}
	return true
}
