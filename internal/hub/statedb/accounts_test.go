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

var accountTime = time.UnixMilli(1_790_000_200_000).UTC()

func configWrite(principal, requestID string, body byte) statedb.ConfigWrite {
	return statedb.ConfigWrite{
		Principal: principal, RequestID: requestID,
		RequestHash: sha256.Sum256([]byte{body}),
		Actor:       statedb.Actor{ID: principal, Kind: "LOCAL_USER", Name: "alice"},
		Now:         accountTime,
	}
}

func openConfigStore(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := stateDir(t)
	create(t, dir)
	return dir, open(t, dir)
}

func accountAuditRows(t *testing.T, dir, action string) []auditRow {
	t.Helper()
	var out []auditRow
	for _, r := range auditRows(t, dir) {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func createOwnerAccount(t *testing.T, s *store.Store, w statedb.ConfigWrite) statedb.AccountStored {
	t.Helper()
	got, err := statedb.CreateOwnerAccount(context.Background(), s, statedb.CreateAccount{
		ConfigWrite: w, ID: "acc-" + w.RequestID, Harness: "opencode", Provider: "anthropic",
		DisplayName: "work", Origin: statedb.AccountOriginOwner,
		Spec: []byte(`{"displayName":"work","reservePercent":"10"}`),
	})
	if err != nil {
		t.Fatalf("CreateOwnerAccount: %v", err)
	}
	if got.Replay {
		t.Fatal("create is not a replay")
	}
	return got
}

func TestOwnerConfigMigrationAddsItsTables(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	db := rawDB(t, dir)
	for _, table := range []string{"accounts", "settings_layers", "config_request_log"} {
		var name string
		if err := db.QueryRowContext(t.Context(),
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Errorf("table %s missing after migration: %v", table, err)
		}
	}
	var hasHubSeq bool
	if err := db.QueryRowContext(t.Context(),
		`SELECT TRUE FROM pragma_table_info('integrations') WHERE name = 'hub_seq'`).Scan(&hasHubSeq); err != nil || !hasHubSeq {
		t.Errorf("integrations.hub_seq missing after migration: %v", hasHubSeq)
	}
}

func TestCreateOwnerAccountStoresVersion1AndAudits(t *testing.T) {
	t.Parallel()
	dir, s := openConfigStore(t)
	got := createOwnerAccount(t, s, configWrite("local:alice", "req-1", 'a'))
	if got.Version != 1 || got.HubSeq <= 0 {
		t.Errorf("stored = %+v, want version 1 with a stamp", got)
	}
	if got.Origin != statedb.AccountOriginOwner {
		t.Errorf("origin = %q, want owner-created", got.Origin)
	}
	readBack, err := statedb.GetAccount(context.Background(), s, "acc-req-1")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if readBack.Version != 1 || string(readBack.Spec) != `{"displayName":"work","reservePercent":"10"}` {
		t.Errorf("read back = %+v, want version 1 with the spec", readBack)
	}
	entries := accountAuditRows(t, dir, "account.create")
	if len(entries) != 1 {
		t.Fatalf("account.create audit rows = %d, want 1", len(entries))
	}
	if entries[0].Target != "accounts/acc-req-1" {
		t.Errorf("audit target = %q, want accounts/acc-req-1", entries[0].Target)
	}
}

func TestCreateOwnerAccountReplaysAndRefusesConflicts(t *testing.T) {
	t.Parallel()
	_, s := openConfigStore(t)
	createOwnerAccount(t, s, configWrite("local:alice", "req-1", 'a'))
	replayed, err := statedb.CreateOwnerAccount(context.Background(), s, statedb.CreateAccount{
		ConfigWrite: configWrite("local:alice", "req-1", 'a'), ID: "acc-other", Harness: "opencode",
		Provider: "anthropic", DisplayName: "other", Origin: statedb.AccountOriginOwner,
		Spec: []byte(`{}`),
	})
	if err != nil || !replayed.Replay {
		t.Fatalf("replay = %+v/%v, want the stored account marked replay", replayed, err)
	}
	if replayed.Harness != "opencode" || replayed.Provider != "anthropic" || replayed.Version != 1 {
		t.Errorf("replay = %+v, want the original fields", replayed)
	}
	if _, err := statedb.CreateOwnerAccount(context.Background(), s, statedb.CreateAccount{
		ConfigWrite: configWrite("local:alice", "req-1", 'b'), ID: "acc-req-1", Harness: "opencode",
		Provider: "anthropic", DisplayName: "work", Origin: statedb.AccountOriginOwner,
		Spec: []byte(`{}`),
	}); !errors.Is(err, statedb.ErrRequestConflict) {
		t.Errorf("conflict = %v, want ErrRequestConflict", err)
	}
	if _, err := statedb.CreateOwnerAccount(context.Background(), s, statedb.CreateAccount{
		ConfigWrite: configWrite("local:alice", "req-2", 'a'), ID: "acc-req-2", Harness: "opencode",
		Provider: "anthropic", DisplayName: "work", Origin: statedb.AccountOriginOwner,
		Spec: []byte(`{}`),
	}); !errors.Is(err, statedb.ErrAccountExists) {
		t.Errorf("duplicate = %v, want ErrAccountExists", err)
	}
}

func TestUpdateAndDeleteOwnerAccount(t *testing.T) {
	t.Parallel()
	_, s := openConfigStore(t)
	ctx := context.Background()
	createOwnerAccount(t, s, configWrite("local:alice", "req-1", 'a'))
	merge := func(current []byte) ([]byte, string, error) {
		if string(current) == "" {
			return nil, "", errors.New("statedb: missing spec")
		}
		return []byte(`{"displayName":"play"}`), "play", nil
	}
	updated, err := statedb.UpdateOwnerAccount(ctx, s, statedb.UpdateAccountOp{
		ConfigWrite: configWrite("local:alice", "req-2", 'c'), ID: "acc-req-1",
		ExpectedVersion: 1, Merge: merge,
	})
	if err != nil || updated.Replay {
		t.Fatalf("update = %+v/%v, want success", updated, err)
	}
	if updated.Version != 2 || updated.DisplayName != "play" {
		t.Errorf("stored = %+v, want version 2 named play", updated)
	}
	var staleErr *statedb.VersionStale
	stale := statedb.UpdateAccountOp{
		ConfigWrite: configWrite("local:alice", "req-3", 'd'), ID: "acc-req-1",
		ExpectedVersion: 1, Merge: merge,
	}
	if _, err := statedb.UpdateOwnerAccount(ctx, s, stale); !errors.As(err, &staleErr) {
		t.Errorf("stale update = %v, want VersionStale", err)
	}
	deleted, err := statedb.DeleteOwnerAccount(ctx, s, statedb.DeleteAccountOp{
		ConfigWrite: configWrite("local:alice", "req-4", 'e'), ID: "acc-req-1",
		ExpectedVersion: 2,
	})
	if err != nil || deleted.Replay {
		t.Fatalf("delete = %+v/%v, want success", deleted, err)
	}
	if _, err := statedb.GetAccount(ctx, s, "acc-req-1"); !errors.Is(err, statedb.ErrAccountUnknown) {
		t.Errorf("after delete = %v, want ErrAccountUnknown", err)
	}
	replayedDelete, err := statedb.DeleteOwnerAccount(ctx, s, statedb.DeleteAccountOp{
		ConfigWrite: configWrite("local:alice", "req-4", 'e'), ID: "acc-req-1",
		ExpectedVersion: 2,
	})
	if err != nil || !replayedDelete.Replay {
		t.Errorf("delete replay = %+v/%v, want the same empty outcome", replayedDelete, err)
	}
	if _, err := statedb.DeleteOwnerAccount(ctx, s, statedb.DeleteAccountOp{
		ConfigWrite: configWrite("local:alice", "req-5", 'f'), ID: "acc-req-1",
	}); !errors.Is(err, statedb.ErrAccountUnknown) {
		t.Errorf("second delete = %v, want ErrAccountUnknown", err)
	}
}

func TestListAccountIDsReadsEveryIdInOrder(t *testing.T) {
	t.Parallel()
	_, s := openConfigStore(t)
	createOwnerAccount(t, s, configWrite("local:alice", "req-1", 'a'))
	if _, err := statedb.CreateOwnerAccount(context.Background(), s, statedb.CreateAccount{
		ConfigWrite: configWrite("local:alice", "req-2", 'b'), ID: "acc-aaa", Harness: "codex",
		Provider: "openai", DisplayName: "other", Origin: statedb.AccountOriginOwner,
		Spec: []byte(`{}`),
	}); err != nil {
		t.Fatalf("CreateOwnerAccount: %v", err)
	}
	got, err := statedb.ListAccountIDs(context.Background(), s)
	if err != nil {
		t.Fatalf("ListAccountIDs: %v", err)
	}
	if len(got) != 2 || got[0] != "acc-aaa" || got[1] != "acc-req-1" {
		t.Errorf("ids = %v, want [acc-aaa acc-req-1]", got)
	}
}
