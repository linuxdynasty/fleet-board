package statedb

import (
	"context"
	"database/sql"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// HookRepo is one watched repo with its hook convention, for the goal
// hooks reconcile (03h) and the EnsureMachineHooks button (row 20).
type HookRepo struct {
	Host    string
	Path    string
	Enabled bool
	// Mode is the owner's hook convention (GENERIC installs, ESHU only
	// reports, AUTO follows the checkout). Unset reads GENERIC: repos
	// watched before the mode existed follow the fleet convention.
	Mode hubv1.RepoHooksMode
}

// ListHookRepos lists every watched repo with its parsed hook mode,
// inside the caller's job. A spec that does not parse is the hub's own
// failure (row 13a wrote it): loud, never a silent skip.
func ListHookRepos(ctx context.Context, tx *sql.Tx) ([]HookRepo, error) {
	rows, err := tx.QueryContext(ctx, `SELECT host, path, enabled, spec_json FROM repos ORDER BY host, path`)
	if err != nil {
		return nil, fmt.Errorf("statedb: list hook repos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []HookRepo
	for rows.Next() {
		var r HookRepo
		var enabled int64
		var spec string
		if err := rows.Scan(&r.Host, &r.Path, &enabled, &spec); err != nil {
			return nil, fmt.Errorf("statedb: scan hook repo: %w", err)
		}
		r.Enabled = enabled != 0
		var parsed hubv1.RepoSpec
		if err := protojson.Unmarshal([]byte(spec), &parsed); err != nil {
			return nil, fmt.Errorf("statedb: hook repo %s/%s spec: %w", r.Host, r.Path, err)
		}
		r.Mode = parsed.GetHooksMode()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("statedb: list hook repos: %w", err)
	}
	return out, nil
}

// LiveGoalHarnesses lists the harnesses holding an unended goal on the
// machine, from the goal specs: no EnsureGoalHooks runs for them (03h
// "never while a goal is live"). A goal whose spec names no harness
// guards nothing (its pane is unknown); fleet goals always name theirs.
func LiveGoalHarnesses(ctx context.Context, tx *sql.Tx, machineID string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT spec FROM goals WHERE machine_id = ? AND end_time IS NULL`, machineID)
	if err != nil {
		return nil, fmt.Errorf("statedb: list live goal harnesses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	live := map[string]bool{}
	for rows.Next() {
		var spec string
		if err := rows.Scan(&spec); err != nil {
			return nil, fmt.Errorf("statedb: scan live goal: %w", err)
		}
		var def hubv1.GoalDefinition
		if err := protojson.Unmarshal([]byte(spec), &def); err != nil {
			return nil, fmt.Errorf("statedb: live goal spec: %w", err)
		}
		if name := def.GetHarness().GetName(); name != "" {
			live[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("statedb: list live goal harnesses: %w", err)
	}
	return live, nil
}
