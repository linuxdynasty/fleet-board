package statedb_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func repoWriteAt(principal, requestID string, body byte, now time.Time) statedb.RepoWrite {
	return statedb.RepoWrite{
		Principal: principal, RequestID: requestID,
		RequestHash: sha256.Sum256([]byte{body}),
		Actor:       statedb.Actor{ID: principal, Kind: "LOCAL_USER", Name: "alice"},
		Now:         now,
	}
}

// TestRepoReplayReturnsTheOriginalResponse proves 02b "Idempotency": the
// same key and body replays the original stored response, not the current
// row. A create retried after a later update answers version 1 with the
// original spec.
func TestRepoReplayReturnsTheOriginalResponse(t *testing.T) {
	t.Parallel()
	s := openRepoStore(t)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	updateRepo(t, s, 1)
	again, err := statedb.CreateWatchedRepo(context.Background(), s, statedb.CreateRepo{
		RepoWrite: repoWrite("local:alice", "req-1", 1),
		Host:      "github.com", Path: "acme/api", CodeHost: "github",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
	})
	if err != nil {
		t.Fatalf("replay create: %v", err)
	}
	if !again.Replay {
		t.Error("same request_id and body does not report Replay")
	}
	if again.Version != 1 {
		t.Errorf("replay version = %d, want the original 1, not the current 2", again.Version)
	}
	if string(again.Spec) != `{"enabled":true}` {
		t.Errorf("replay spec = %q, want the original, not the update", again.Spec)
	}
}

// TestRepoReplayExpiresAfter24h proves the 02b window: past 24 h the key is
// free again. A create retried after its delete, past the window, watches
// the repo anew instead of replaying the old tombstone.
func TestRepoReplayExpiresAfter24h(t *testing.T) {
	t.Parallel()
	s := openRepoStore(t)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	if _, err := statedb.DeleteWatchedRepo(context.Background(), s, statedb.DeleteRepo{
		RepoWrite: repoWrite("local:alice", "req-2", 6),
		Host:      "github.com", Path: "acme/api", ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	late := repoTime.Add(25 * time.Hour)
	got, err := statedb.CreateWatchedRepo(context.Background(), s, statedb.CreateRepo{
		RepoWrite: repoWriteAt("local:alice", "req-1", 1, late),
		Host:      "github.com", Path: "acme/api", CodeHost: "github",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
	})
	if err != nil {
		t.Fatalf("recreate past the window: %v", err)
	}
	if got.Replay {
		t.Error("a key past the 24 h window still replays")
	}
	if got.Version != 1 {
		t.Errorf("version = %d, want a fresh 1", got.Version)
	}
}

// TestExpiredRepoRequestsArePruned proves the table cannot grow without
// bound: a write past the window sweeps expired rows.
func TestExpiredRepoRequestsArePruned(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	late := repoTime.Add(25 * time.Hour)
	if _, err := statedb.CreateWatchedRepo(context.Background(), s, statedb.CreateRepo{
		RepoWrite: repoWriteAt("local:alice", "req-9", 9, late),
		Host:      "github.com", Path: "acme/other", CodeHost: "github",
		Enabled: true, Spec: []byte(`{"enabled":true}`),
	}); err != nil {
		t.Fatalf("later create: %v", err)
	}
	if n := rawCount(t, statedb.Path(dir), `SELECT COUNT(*) FROM repo_request_log`); n != 1 {
		t.Errorf("%d idempotency rows remain, want 1 (the expired req-1 pruned)", n)
	}
}

func seedWorkItem(t *testing.T, dir, integration, project, key string, stage int) {
	t.Helper()
	if _, err := rawDB(t, dir).ExecContext(t.Context(), `INSERT INTO work_items(key, integration, kind,
		project, item_key, item_json, update_ms, stage, trust_json, manual_reason,
		version, create_ms, last_sync_ms) VALUES (?, ?, 'ISSUE', ?, ?, '{}', 1, ?, '{}', 0, 1, 1, 1)`,
		integration+"/ISSUE/"+project+"/"+key, integration, project, key, stage); err != nil {
		t.Fatal(err)
	}
}

func workItemStage(t *testing.T, dir, key string) int {
	t.Helper()
	var stage int
	if err := rawDB(t, dir).QueryRowContext(t.Context(),
		`SELECT stage FROM work_items WHERE item_key = ?`, key).Scan(&stage); err != nil {
		t.Fatal(err)
	}
	return stage
}

// TestDeleteUnreadiesWorkItems proves repo.proto: a delete moves the repo's
// READY mirrors out of READY in the same transaction. Other repos' READY
// rows and non-READY rows stay.
func TestDeleteUnreadiesWorkItems(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	createRepo(t, s, repoWrite("local:alice", "req-1", 1))
	seedWorkItem(t, dir, "github", "acme/api", "7", 2)
	seedWorkItem(t, dir, "github", "acme/other", "8", 2)
	seedWorkItem(t, dir, "github", "acme/api", "9", 1)
	if _, err := statedb.DeleteWatchedRepo(context.Background(), s, statedb.DeleteRepo{
		RepoWrite: repoWrite("local:alice", "req-2", 6),
		Host:      "github.com", Path: "acme/api", ExpectedVersion: 1,
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := workItemStage(t, dir, "7"); got != 1 {
		t.Errorf("deleted repo's READY item stage = %d, want SEEN (1)", got)
	}
	if got := workItemStage(t, dir, "8"); got != 2 {
		t.Errorf("other repo's READY item stage = %d, want READY (2)", got)
	}
	if got := workItemStage(t, dir, "9"); got != 1 {
		t.Errorf("SEEN item stage = %d, want SEEN (1)", got)
	}
}
