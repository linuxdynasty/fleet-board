package statedb_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

var repoTime = time.UnixMilli(1_790_000_100_000).UTC()

func repoWrite(principal, requestID string, body byte) statedb.RepoWrite {
	return statedb.RepoWrite{
		Principal: principal, RequestID: requestID,
		RequestHash: sha256.Sum256([]byte{body}),
		Actor:       statedb.Actor{ID: principal, Kind: "LOCAL_USER", Name: "alice"},
		Now:         repoTime,
	}
}

func openRepoStore(t *testing.T) *store.Store {
	t.Helper()
	dir := stateDir(t)
	create(t, dir)
	return open(t, dir)
}

func createRepo(t *testing.T, s *store.Store, w statedb.RepoWrite) statedb.RepoStored {
	t.Helper()
	got, err := statedb.CreateWatchedRepo(context.Background(), s, statedb.CreateRepo{
		RepoWrite: w,
		Host:      "github.com", Path: "acme/api", CodeHost: "github",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
	}, nil)
	if err != nil {
		t.Fatalf("CreateWatchedRepo: %v", err)
	}
	return got
}

func repoRow(t *testing.T, dir string) (spec string, version, hubSeq, enabled int64) {
	t.Helper()
	err := rawDB(t, dir).QueryRowContext(t.Context(),
		`SELECT spec_json, version, hub_seq, enabled FROM repos WHERE host = 'github.com' AND path = 'acme/api'`,
	).Scan(&spec, &version, &hubSeq, &enabled)
	if err != nil {
		t.Fatalf("read repos row: %v", err)
	}
	return spec, version, hubSeq, enabled
}

func TestCreateWatchedRepoInsertsVersion1AndAudits(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	stored, err := statedb.CreateWatchedRepo(context.Background(), s, statedb.CreateRepo{
		RepoWrite: repoWrite("local:alice", "req-1", 1),
		Host:      "github.com", Path: "acme/api", CodeHost: "github",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
	}, nil)
	if err != nil {
		t.Fatalf("CreateWatchedRepo: %v", err)
	}
	if stored.Replay {
		t.Error("first create reports Replay")
	}
	if stored.Version != 1 || stored.HubSeq <= 0 {
		t.Errorf("stored = version %d hub_seq %d, want 1 and > 0", stored.Version, stored.HubSeq)
	}
	spec, version, hubSeq, enabled := repoRow(t, dir)
	if spec != `{"enabled":true}` || version != 1 || enabled != 1 {
		t.Errorf("row = %q v%d enabled=%d", spec, version, enabled)
	}
	if hubSeq != stored.HubSeq {
		t.Errorf("row hub_seq %d, want the stamped %d", hubSeq, stored.HubSeq)
	}
	rows := auditRows(t, dir)
	if len(rows) != 1 {
		t.Fatalf("%d audit rows, want 1", len(rows))
	}
	r := rows[0]
	if r.Action != "repo.create" || r.Target != "repos/github.com/acme/api" {
		t.Errorf("audit = %q %q", r.Action, r.Target)
	}
	if r.Request != "req-1" || r.ActorID != "local:alice" || r.Outcome != "OK" {
		t.Errorf("audit actor %q request %q outcome %q", r.ActorID, r.Request, r.Outcome)
	}
}

func TestCreateWatchedRepoReplayReturnsTheStoredRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	first := createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	again, err := statedb.CreateWatchedRepo(context.Background(), s, statedb.CreateRepo{
		RepoWrite: repoWrite("local:alice", "req-1", 1),
		Host:      "github.com", Path: "acme/api", CodeHost: "github",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
	}, nil)
	if err != nil {
		t.Fatalf("replay create: %v", err)
	}
	if !again.Replay {
		t.Error("same request_id and body does not report Replay")
	}
	if again.Version != first.Version || again.HubSeq != first.HubSeq {
		t.Errorf("replay = %+v, want %+v", again, first)
	}
	if rows := auditRows(t, dir); len(rows) != 1 {
		t.Errorf("replay wrote %d audit rows, want 1", len(rows))
	}
}

func TestCreateWatchedRepoRefusesAReusedRequestID(t *testing.T) {
	t.Parallel()
	s := openRepoStore(t)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	_, err := statedb.CreateWatchedRepo(context.Background(), s, statedb.CreateRepo{
		RepoWrite: repoWrite("local:alice", "req-1", 2),
		Host:      "github.com", Path: "acme/api", CodeHost: "github",
		Enabled: true, Spec: []byte(`{"enabled":false}`),
	}, nil)
	if !errors.Is(err, statedb.ErrRequestConflict) {
		t.Errorf("reused request_id with another body = %v, want ErrRequestConflict", err)
	}
}

func TestCreateWatchedRepoRefusesAnExistingRepo(t *testing.T) {
	t.Parallel()
	s := openRepoStore(t)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	_, err := statedb.CreateWatchedRepo(context.Background(), s, statedb.CreateRepo{
		RepoWrite: repoWrite("local:alice", "req-2", 1),
		Host:      "github.com", Path: "acme/api", CodeHost: "github",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
	}, nil)
	if !errors.Is(err, statedb.ErrRepoExists) {
		t.Errorf("second create = %v, want ErrRepoExists", err)
	}
}

func updateRepo(t *testing.T, s *store.Store, version uint64) statedb.RepoStored {
	t.Helper()
	got, err := statedb.UpdateWatchedRepo(context.Background(), s, statedb.UpdateRepo{
		RepoWrite:       repoWrite("local:alice", "req-2", 3),
		Host:            "github.com",
		Path:            "acme/api",
		Enabled:         false,
		Spec:            []byte(`{"enabled":false}`),
		ExpectedVersion: version,
	})
	if err != nil {
		t.Fatalf("UpdateWatchedRepo: %v", err)
	}
	return got
}

func TestUpdateWatchedRepoBumpsTheVersionAndAudits(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	got := updateRepo(t, s, 1)
	if got.Version != 2 || got.Replay {
		t.Errorf("update = %+v, want version 2, no replay", got)
	}
	if spec, version, _, enabled := repoRow(t, dir); spec != `{"enabled":false}` || version != 2 || enabled != 0 {
		t.Errorf("row = %q v%d enabled=%d", spec, version, enabled)
	}
	rows := auditRows(t, dir)
	if len(rows) != 2 || rows[1].Action != "repo.update" {
		t.Errorf("%d audit rows, last = %+v, want repo.update second", len(rows), rows)
	}
}

func TestUpdateWatchedRepoSkipsTheCheckAtVersionZero(t *testing.T) {
	t.Parallel()
	s := openRepoStore(t)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	updateRepo(t, s, 1)
	got, err := statedb.UpdateWatchedRepo(context.Background(), s, statedb.UpdateRepo{
		RepoWrite: repoWrite("local:alice", "req-3", 4),
		Host:      "github.com", Path: "acme/api",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
		ExpectedVersion: 0,
	})
	if err != nil {
		t.Fatalf("update at version 0: %v", err)
	}
	if got.Version != 3 {
		t.Errorf("version = %d, want 3", got.Version)
	}
}

func TestUpdateWatchedRepoRefusesAStaleVersion(t *testing.T) {
	t.Parallel()
	s := openRepoStore(t)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	updateRepo(t, s, 1)
	_, err := statedb.UpdateWatchedRepo(context.Background(), s, statedb.UpdateRepo{
		RepoWrite: repoWrite("local:alice", "req-9", 5),
		Host:      "github.com", Path: "acme/api",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
		ExpectedVersion: 1,
	})
	var conflict *statedb.VersionConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("stale update = %v, want VersionConflict", err)
	}
	if conflict.Current != 2 {
		t.Errorf("current = %d, want 2", conflict.Current)
	}
}

func TestUpdateWatchedRepoRefusesAnUnknownRepo(t *testing.T) {
	t.Parallel()
	s := openRepoStore(t)
	_, err := statedb.UpdateWatchedRepo(context.Background(), s, statedb.UpdateRepo{
		RepoWrite: repoWrite("local:alice", "req-1", 1),
		Host:      "github.com", Path: "acme/missing",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
		ExpectedVersion: 1,
	})
	if !errors.Is(err, statedb.ErrRepoUnknown) {
		t.Errorf("update of a missing repo = %v, want ErrRepoUnknown", err)
	}
}

func TestDeleteWatchedRepoRemovesTheRowAndAudits(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	got, err := statedb.DeleteWatchedRepo(context.Background(), s, statedb.DeleteRepo{
		RepoWrite: repoWrite("local:alice", "req-2", 6),
		Host:      "github.com", Path: "acme/api", ExpectedVersion: 1,
	}, nil)
	if err != nil {
		t.Fatalf("DeleteWatchedRepo: %v", err)
	}
	if got.Replay {
		t.Error("first delete reports Replay")
	}
	n := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM repos`)
	if n != 0 {
		t.Errorf("%d repos rows remain", n)
	}
	rows := auditRows(t, dir)
	if len(rows) != 2 || rows[1].Action != "repo.delete" || rows[1].Target != "repos/github.com/acme/api" {
		t.Errorf("audit rows = %+v, want repo.delete second", rows)
	}
}

func TestDeleteWatchedRepoReplayDeletesNothingTwice(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	w := repoWrite("local:alice", "req-2", 6)
	if _, err := statedb.DeleteWatchedRepo(context.Background(), s, statedb.DeleteRepo{
		RepoWrite: w, Host: "github.com", Path: "acme/api", ExpectedVersion: 1,
	}, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	again, err := statedb.DeleteWatchedRepo(context.Background(), s, statedb.DeleteRepo{
		RepoWrite: w, Host: "github.com", Path: "acme/api", ExpectedVersion: 1,
	}, nil)
	if err != nil {
		t.Fatalf("replay delete: %v", err)
	}
	if !again.Replay {
		t.Error("same delete twice does not report Replay")
	}
	if rows := auditRows(t, dir); len(rows) != 2 {
		t.Errorf("replay delete wrote %d audit rows, want 2", len(rows))
	}
}

func TestOwnerRepoWritesMigrateFromTheSyncSchema(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	s, err := store.Open(t.Context(), store.Options{Path: statedb.Path(dir), Name: "state", Migrations: statedb.Migrations()[:14]})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(statedb.Path(dir), 0o600); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, dir)
	if _, err := db.ExecContext(t.Context(), `INSERT INTO repos(host, path, code_host, enabled,
		spec_json, version, create_ms, update_ms) VALUES ('github.com', 'acme/api', 'github', 1, '{}', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, dir)
	got, err := statedb.UpdateWatchedRepo(context.Background(), s, statedb.UpdateRepo{
		RepoWrite: repoWrite("local:alice", "req-1", 7),
		Host:      "github.com", Path: "acme/api",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
		ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatalf("update after migration: %v", err)
	}
	if got.Version != 2 || got.HubSeq <= 0 {
		t.Errorf("after migration = %+v, want version 2 with a stamp", got)
	}
}
