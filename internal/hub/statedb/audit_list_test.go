package statedb_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// The audit list helper reads back every stored audit row, so the hub API
// can filter, sort and page entries in memory.
func TestAuditListReadsEveryRow(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	at := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

	if err := statedb.WriteAudit(ctx, s,
		statedb.AuditEntry{
			Actor:  statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"},
			Action: "repo.create", Target: "repos/h/p", MachineID: "m-1",
			RequestID: "req-1", Detail: `{"n":1}`, Now: at,
		},
		statedb.AuditEntry{
			Actor:  statedb.Actor{ID: "local:alice", Kind: "LOCAL_USER", Name: "alice"},
			Action: "repo.delete", Target: "repos/h/q",
			RequestID: "req-2", Outcome: "FAILED", Reason: "RESOURCE_NOT_FOUND",
			Detail: `{"n":2}`, Now: at.Add(time.Minute),
		},
	); err != nil {
		t.Fatalf("WriteAudit: %v", err)
	}

	rows, err := statedb.ListStoredAuditEntries(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows %d, want 2 audit entries", len(rows))
	}
	got := rows[0]
	if got.Action != "repo.create" || got.Target != "repos/h/p" || got.MachineID != "m-1" {
		t.Errorf("row %+v, want the repo.create row with its target and machine", got)
	}
	if got.ActorID != "local:alice" || got.ActorKind != "LOCAL_USER" || got.CreateMs != at.UnixMilli() {
		t.Errorf("row %+v, want the actor and the write time", got)
	}
	if got.Outcome != "OK" || rows[1].Outcome != "FAILED" || rows[1].Reason != "RESOURCE_NOT_FOUND" {
		t.Errorf("outcomes %q/%q, want OK then FAILED with its reason", got.Outcome, rows[1].Outcome)
	}
}
