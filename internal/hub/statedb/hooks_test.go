package statedb_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

var hookNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func seedHookRepoState(t *testing.T, s *store.Store, host, path, mode string, enabled bool) {
	t.Helper()
	spec := `{"repo":{"host":"` + host + `","path":"` + path + `"},"hooksMode":"` + mode + `"}`
	// CreateWatchedRepo submits its own writer job: never wrap it in one.
	_, err := statedb.CreateWatchedRepo(context.Background(), s, statedb.CreateRepo{
		RepoWrite: statedb.RepoWrite{Principal: "local:alice", RequestID: "req-" + path, Now: hookNow},
		Host:      host, Path: path, CodeHost: "github", Enabled: enabled, Spec: []byte(spec),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func seedHookGoal(t *testing.T, s *store.Store, id, machine, harness string, ended bool) {
	t.Helper()
	row := observedRow(id)
	row.MachineID = machine
	row.Spec = `{"harness":{"name":"` + harness + `"}}`
	if ended {
		ms := int64(2000)
		row.EndMs = &ms
	}
	writeObserved(t, s, row)
}

// TestHookInputs: the reconcile reads watched repos with their hook
// modes and the harnesses holding live goals.
func TestHookInputs(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	seedHookRepoState(t, s, "github.com", "acme/web", "REPO_HOOKS_MODE_GENERIC", true)
	seedHookRepoState(t, s, "github.com", "acme/eshu", "REPO_HOOKS_MODE_ESHU", true)
	seedHookGoal(t, s, "g1", "m1", "codex", false)
	seedHookGoal(t, s, "g2", "m1", "claude", true)

	var repos []statedb.HookRepo
	var live map[string]bool
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		repos, err = statedb.ListHookRepos(context.Background(), tx)
		if err != nil {
			return err
		}
		live, err = statedb.LiveGoalHarnesses(context.Background(), tx, "m1")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("repos = %+v, want 2", repos)
	}
	modes := map[string]hubv1.RepoHooksMode{}
	for _, r := range repos {
		modes[r.Path] = r.Mode
		if !r.Enabled || r.Host != "github.com" {
			t.Fatalf("repo = %+v", r)
		}
	}
	if modes["acme/web"] != hubv1.RepoHooksMode_REPO_HOOKS_MODE_GENERIC ||
		modes["acme/eshu"] != hubv1.RepoHooksMode_REPO_HOOKS_MODE_ESHU {
		t.Fatalf("modes = %+v", modes)
	}
	if !live["codex"] || live["claude"] || len(live) != 1 {
		t.Fatalf("live = %+v, want only codex", live)
	}
}
