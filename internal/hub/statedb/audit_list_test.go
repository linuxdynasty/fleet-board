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

// The audit tail reads only rows past a position in hub_seq order, and the
// max reports the highest stamped row, so a Watch publisher can tail the
// log without re-reading what it already sent.
func TestAuditTailReadsOnlyPastAPosition(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	s := open(t, dir)
	ctx := context.Background()
	at := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)
	mk := func(action, target string) statedb.AuditEntry {
		return statedb.AuditEntry{Action: action, Target: target, Now: at}
	}
	if err := statedb.WriteAudit(ctx, s, mk("a.one", "t/1"), mk("a.two", "t/2")); err != nil {
		t.Fatalf("WriteAudit: %v", err)
	}
	max, err := statedb.MaxAuditSeq(ctx, s)
	if err != nil {
		t.Fatalf("MaxAuditSeq: %v", err)
	}
	if max <= 0 {
		t.Fatalf("max audit seq = %d, want positive", max)
	}
	rows, err := statedb.ListAuditSince(ctx, s, max)
	if err != nil {
		t.Fatalf("ListAuditSince: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows past max = %d, want none", len(rows))
	}
	if err := statedb.WriteAudit(ctx, s, mk("a.three", "t/3")); err != nil {
		t.Fatalf("WriteAudit: %v", err)
	}
	rows, err = statedb.ListAuditSince(ctx, s, max)
	if err != nil {
		t.Fatalf("ListAuditSince: %v", err)
	}
	if len(rows) != 1 || rows[0].Action != "a.three" || rows[0].HubSeq <= max {
		t.Fatalf("rows past max = %+v, want only a.three above %d", rows, max)
	}
	if got, err := statedb.MaxAuditSeq(ctx, s); err != nil || got != rows[0].HubSeq {
		t.Fatalf("max audit seq = %d, %v; want %d", got, err, rows[0].HubSeq)
	}
}
