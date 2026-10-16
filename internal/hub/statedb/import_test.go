package statedb_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
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
				Enabled: false, Spec: []byte(`{"enabled":false}`), ExpectedVersion: 1,
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

func TestApplyConfigImportRefusesAStaleUpdate(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	createRepo(t, s, repoWrite("local:alice", "seed-1", 1))
	_, _, err := statedb.ApplyConfigImport(context.Background(), s, statedb.ConfigImport{
		ImportWrite: importWrite("local:alice", "imp-1", 7, importTime),
		PlanHash:    "abc123",
		Ops: []statedb.ImportRepoOp{{
			Host: "github.com", Path: "acme/api", CodeHost: "github",
			Enabled: false, Spec: []byte(`{"enabled":false}`), ExpectedVersion: 2,
		}},
		Response: []byte(`{}`),
	})
	if !errors.Is(err, statedb.ErrImportStale) {
		t.Fatalf("stale update = %v, want ErrImportStale", err)
	}
	spec, version, _, _ := repoRow(t, dir)
	if spec != `{"enabled":true}` || version != 1 {
		t.Errorf("row = %q v%d after a stale apply, want the seed spec at v1", spec, version)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM audit_log`); got != 1 {
		t.Errorf("audit rows = %d after a stale apply, want only the seed", got)
	}
}

// enrollLaptop enrolls one machine named laptop (version 1) for the
// machine import apply tests.
func enrollLaptop(t *testing.T, s *store.Store, id string) {
	t.Helper()
	key := enrollKey("install-1", 1)
	if _, err := statedb.Enroll(context.Background(), s, newMachine(key, id, beforeTTL)); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
}

func machineLabels(t *testing.T, s *store.Store, id string) (labels string, version int64) {
	t.Helper()
	ctx := context.Background()
	if err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT labels, version FROM machines WHERE machine_id = ?`,
			id).Scan(&labels, &version)
	}); err != nil {
		t.Fatal(err)
	}
	return labels, version
}

func TestApplyConfigImportAppliesAMachineUpdateAtItsVersion(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	enrollLaptop(t, s, "01jmachine0000000000000001")
	_, _, err := statedb.ApplyConfigImport(context.Background(), s, statedb.ConfigImport{
		ImportWrite: importWrite("local:alice", "imp-1", 7, importTime),
		PlanHash:    "abc123",
		MachineOps: []statedb.ImportMachineOp{{
			MachineID: "01jmachine0000000000000001", Name: "laptop",
			Labels: []string{"field"}, ExpectedVersion: 1,
		}},
		Response: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("fresh machine apply = %v, want no error", err)
	}
	labels, version := machineLabels(t, s, "01jmachine0000000000000001")
	if labels != `["field"]` || version != 2 {
		t.Errorf("machine = %q v%d, want [field] v2", labels, version)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM audit_log WHERE action = 'config.import'`); got != 1 {
		t.Errorf("config.import rows = %d, want 1", got)
	}
}

// TestApplyConfigImportRefusesAStaleMachineUpdate is the
// UpdateMachine-after-baseline race: an owner write that lands after the
// import's baseline but before the apply aborts the whole apply instead
// of losing the concurrent labels.
func TestApplyConfigImportRefusesAStaleMachineUpdate(t *testing.T) {
	t.Parallel()
	dir, s := tokenStore(t)
	enrollLaptop(t, s, "01jmachine0000000000000001")
	if _, err := statedb.UpdateMachine(context.Background(), s, statedb.MachineUpdate{
		MachineWrite: machineWrite("local:alice", "req-2", 2, importTime),
		MachineID:    "01jmachine0000000000000001", ExpectedVersion: 1,
		Labels: []string{"desk"}, HasLabels: true,
	}); err != nil {
		t.Fatalf("concurrent UpdateMachine: %v", err)
	}
	_, _, err := statedb.ApplyConfigImport(context.Background(), s, statedb.ConfigImport{
		ImportWrite: importWrite("local:alice", "imp-1", 7, importTime),
		PlanHash:    "abc123",
		MachineOps: []statedb.ImportMachineOp{{
			MachineID: "01jmachine0000000000000001", Name: "laptop",
			Labels: []string{"gpu"}, ExpectedVersion: 1,
		}},
		Response: []byte(`{}`),
	})
	if !errors.Is(err, statedb.ErrImportStale) {
		t.Fatalf("stale machine apply = %v, want ErrImportStale", err)
	}
	labels, version := machineLabels(t, s, "01jmachine0000000000000001")
	if labels != `["desk"]` || version != 2 {
		t.Errorf("machine = %q v%d after a stale apply, want the concurrent [desk] v2", labels, version)
	}
	if got := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM audit_log WHERE action = 'config.import'`); got != 0 {
		t.Errorf("config.import rows = %d after a stale apply, want 0", got)
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
