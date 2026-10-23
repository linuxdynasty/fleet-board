package statedb_test

import (
	"context"
	"errors"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func TestValidatedWriteFollowsRepoChecksAndPrecedesEffects(t *testing.T) {
	t.Parallel()
	dir, s := openConfigStore(t)
	deny := errors.New("quota exhausted")
	checks := 0
	ctx := statedb.WithValidatedWrite(context.Background(), func() error {
		checks++
		return deny
	})
	create := func(request string, body byte) error {
		_, err := statedb.CreateWatchedRepo(ctx, s, statedb.CreateRepo{
			RepoWrite: repoWrite("local:alice", request, body),
			Host:      "github.com", Path: "acme/api", CodeHost: "github", Enabled: true,
			Spec: []byte(`{"enabled":true}`),
		}, nil)
		return err
	}
	if err := create("first", 1); !errors.Is(err, deny) {
		t.Fatalf("new repo = %v, want quota denial", err)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM repos`); got != 0 {
		t.Fatalf("denied create wrote %d repos", got)
	}
	createRepo(t, s, repoWrite("local:alice", "seed", 2))
	if err := create("new", 3); !errors.Is(err, statedb.ErrRepoExists) {
		t.Errorf("existing repo = %v, want resource conflict before quota", err)
	}
	if checks != 1 {
		t.Errorf("checkpoint calls = %d, want 1", checks)
	}
	if err := create("seed", 2); !errors.Is(err, deny) {
		t.Errorf("valid replay = %v, want quota denial", err)
	}
	if err := create("seed", 4); !errors.Is(err, statedb.ErrRequestConflict) {
		t.Errorf("conflicting replay = %v, want conflict before quota", err)
	}
	if checks != 2 {
		t.Errorf("checkpoint calls = %d, want 2", checks)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM audit_log`); got != 1 {
		t.Errorf("audit rows = %d, want only seed create", got)
	}
}

func TestImportChecksEveryOperationBeforeQuotaAndMutation(t *testing.T) {
	t.Parallel()
	dir, s := openConfigStore(t)
	createRepo(t, s, repoWrite("local:alice", "seed", 1))
	deny := errors.New("quota exhausted")
	checks := 0
	ctx := statedb.WithValidatedWrite(context.Background(), func() error {
		checks++
		return deny
	})
	apply := func(ops []statedb.ImportRepoOp, request string) error {
		_, _, err := statedb.ApplyConfigImport(ctx, s, statedb.ConfigImport{
			ImportWrite: importWrite("local:alice", request, 3, importTime),
			Ops:         ops, Response: []byte(`{"applied":true}`),
		})
		return err
	}
	stale := []statedb.ImportRepoOp{
		importCreateOp("github.com", "acme/web"),
		{Host: "github.com", Path: "acme/api", ExpectedVersion: 2},
	}
	if err := apply(stale, "stale"); !errors.Is(err, statedb.ErrImportStale) {
		t.Fatalf("stale later op = %v, want stale before quota", err)
	}
	if checks != 0 {
		t.Fatalf("stale import charged %d times", checks)
	}
	valid := []statedb.ImportRepoOp{importCreateOp("github.com", "acme/web")}
	if err := apply(valid, "new"); !errors.Is(err, deny) {
		t.Fatalf("valid import = %v, want quota denial", err)
	}
	if checks != 1 {
		t.Errorf("checkpoint calls = %d, want 1", checks)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM repos`); got != 1 {
		t.Errorf("repos = %d, want only seeded repo", got)
	}
}
