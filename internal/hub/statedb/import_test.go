package statedb_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

var importTime = time.UnixMilli(1_790_000_200_000).UTC()

func importWrite(principal, requestID string, body byte, now time.Time) statedb.ImportWrite {
	return statedb.ImportWrite{
		Principal: principal, RequestID: requestID,
		RequestHash: sha256.Sum256([]byte{body}),
		Actor:       statedb.Actor{ID: principal, Kind: "LOCAL_USER", Name: "alice"},
		Now:         now,
	}
}

func importApply(t *testing.T, s *store.Store, w statedb.ImportWrite, ops []statedb.ImportRepoOp) ([]byte, bool) {
	t.Helper()
	resp, replay, err := statedb.ApplyConfigImport(context.Background(), s, statedb.ConfigImport{
		ImportWrite: w,
		PlanHash:    "abc123",
		Ops:         ops,
		Response:    []byte(`{"applied":true,"plan_hash":"abc123"}`),
	})
	if err != nil {
		t.Fatalf("ApplyConfigImport: %v", err)
	}
	return resp, replay
}

func importCreateOp(host, path string) statedb.ImportRepoOp {
	return statedb.ImportRepoOp{
		Host: host, Path: path, CodeHost: "github",
		Enabled: true, Spec: []byte(`{"enabled":true}`), Create: true,
	}
}

func TestApplyConfigImportWritesReposAndOneAuditRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	createRepo(t, s, repoWrite("local:alice", "seed-1", 1))
	resp, replay := importApply(t, s,
		importWrite("local:alice", "imp-1", 7, importTime),
		[]statedb.ImportRepoOp{
			importCreateOp("github.com", "acme/web"),
			{
				Host: "github.com", Path: "acme/api", CodeHost: "github",
				Enabled: false, Spec: []byte(`{"enabled":false}`),
			},
		})
	if replay || string(resp) != `{"applied":true,"plan_hash":"abc123"}` {
		t.Errorf("apply = replay %v %q, want the stored response, not a replay", replay, resp)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM repos`); got != 2 {
		t.Errorf("repos = %d, want 2", got)
	}
	spec, version, _, enabled := repoRow(t, dir)
	if spec != `{"enabled":false}` || version != 2 || enabled != 0 {
		t.Errorf("updated row = %q v%d enabled=%d, want the imported spec at v2 disabled", spec, version, enabled)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM audit_log WHERE action = 'config.import'`); got != 1 {
		t.Errorf("config.import rows = %d, want 1", got)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM audit_log`); got != 2 {
		t.Errorf("audit rows = %d, want the seed create plus one config.import", got)
	}
}

func TestApplyConfigImportIsOneTransaction(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	createRepo(t, s, repoWrite("local:alice", "seed-1", 1))
	_, _, err := statedb.ApplyConfigImport(context.Background(), s, statedb.ConfigImport{
		ImportWrite: importWrite("local:alice", "imp-1", 7, importTime),
		PlanHash:    "abc123",
		Ops: []statedb.ImportRepoOp{
			importCreateOp("github.com", "acme/web"),
			importCreateOp("github.com", "acme/api"),
		},
		Response: []byte(`{}`),
	})
	if !errors.Is(err, statedb.ErrRepoExists) {
		t.Fatalf("clashing apply = %v, want ErrRepoExists", err)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM repos`); got != 1 {
		t.Errorf("repos = %d after a failed apply, want only the seed", got)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM audit_log WHERE action = 'config.import'`); got != 0 {
		t.Errorf("config.import rows = %d after a failed apply, want 0", got)
	}
}

func TestApplyConfigImportReplaysConflictsAndExpires(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	w := importWrite("local:alice", "imp-1", 7, importTime)
	first, _ := importApply(t, s, w, []statedb.ImportRepoOp{importCreateOp("github.com", "acme/web")})
	second, replay := importApply(t, s, w, []statedb.ImportRepoOp{importCreateOp("github.com", "acme/other")})
	if !replay || string(second) != string(first) {
		t.Error("same request_id and body did not replay the stored response")
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM repos`); got != 1 {
		t.Errorf("repos = %d after a replay, want 1", got)
	}
	other := importWrite("local:alice", "imp-1", 8, importTime)
	_, _, err := statedb.ApplyConfigImport(context.Background(), s, statedb.ConfigImport{
		ImportWrite: other, Response: []byte(`{}`),
	})
	if !errors.Is(err, statedb.ErrRequestConflict) {
		t.Errorf("another body on the same request_id = %v, want ErrRequestConflict", err)
	}
	// Past the 24 h idempotency window the key is free: the same call is
	// no longer a conflict (it fails later, on the existing repo).
	late := importWrite("local:alice", "imp-1", 8, importTime.Add(25*time.Hour))
	_, _, err = statedb.ApplyConfigImport(context.Background(), s, statedb.ConfigImport{
		ImportWrite: late,
		Ops:         []statedb.ImportRepoOp{importCreateOp("github.com", "acme/web")},
		Response:    []byte(`{}`),
	})
	if !errors.Is(err, statedb.ErrRepoExists) {
		t.Errorf("expired key reuse = %v, want the apply to run (ErrRepoExists)", err)
	}
}
