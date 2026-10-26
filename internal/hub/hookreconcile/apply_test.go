package hookreconcile

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// hookRig is a raw database with the full migration chain: the
// applier's tables (repos, goals, commands, machines) without a hub.
type hookRig struct {
	t  *testing.T
	db *sql.DB
}

func newHookRig(t *testing.T) *hookRig {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, m := range statedb.Migrations() {
		if _, err := db.ExecContext(t.Context(), m.SQL); err != nil {
			t.Fatalf("migration %s: %v", m.Name, err)
		}
	}
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `INSERT INTO machines(machine_id, name, expected_hostname, drained,
		install_id, state, hostname_mismatch, created_time) VALUES ('m1', 'm1', 'm1', 0, 'i', 'ENROLLED', 0, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO repos(host, path, code_host, enabled, spec_json, version,
		create_ms, update_ms) VALUES ('github.com', 'acme/web', 'github', 1,
		'{"repo":{"host":"github.com","path":"acme/web"},"hooksMode":"REPO_HOOKS_MODE_GENERIC"}', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	return &hookRig{t: t, db: db}
}

func hookSnap() *agentv1.Snapshot {
	return &agentv1.Snapshot{
		Harnesses: []*agentv1.HarnessInfo{
			{Harness: &typev1.Harness{Name: "codex"}, LaunchAllowed: true},
		},
		Repos: []*agentv1.RepoCheckout{
			{Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/web"}},
		},
	}
}

func (r *hookRig) apply(snap *agentv1.Snapshot) {
	r.t.Helper()
	a := Applier{Now: func() time.Time { return time.Unix(3000, 0).UTC() }, Expected: map[string]string{"codex": "v3"}}
	tx, err := r.db.BeginTx(r.t.Context(), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, _, err := a.Apply(context.Background(), tx, "m1", snap); err != nil {
		_ = tx.Rollback()
		r.t.Fatalf("Apply: %v", err)
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
}

func (r *hookRig) hookCommands() int {
	r.t.Helper()
	var n int
	if err := r.db.QueryRowContext(r.t.Context(), `SELECT COUNT(*) FROM commands WHERE kind = 'ensure_goal_hooks'`).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func TestApplyEnqueuesMissingAndThrottles(t *testing.T) {
	t.Parallel()
	r := newHookRig(t)
	r.apply(hookSnap())
	if got := r.hookCommands(); got != 1 {
		t.Fatalf("commands = %d, want 1", got)
	}
	// The next snapshot inside the throttle window sends nothing new.
	r.apply(hookSnap())
	if got := r.hookCommands(); got != 1 {
		t.Fatalf("resend inside window: commands = %d, want 1", got)
	}
}

func TestApplyHealthyNoop(t *testing.T) {
	t.Parallel()
	r := newHookRig(t)
	snap := hookSnap()
	snap.Harnesses[0].GoalHooks = &typev1.GoalHooksState{Installed: true, Verified: true, HookVersion: "v3"}
	snap.Repos[0].GoalHooks = []*typev1.HarnessGoalHooks{
		{Harness: &typev1.Harness{Name: "codex"}, State: &typev1.GoalHooksState{Installed: true, Verified: true, HookVersion: "v3"}},
	}
	r.apply(snap)
	if got := r.hookCommands(); got != 0 {
		t.Fatalf("healthy install enqueued %d commands", got)
	}
}
