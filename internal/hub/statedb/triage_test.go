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

var triageTime = time.UnixMilli(1_790_000_300_000).UTC()

func triageWrite(principal, requestID string, body byte) statedb.TriageWrite {
	return statedb.TriageWrite{
		Principal: principal, RequestID: requestID,
		RequestHash: sha256.Sum256([]byte{body}),
		Actor:       statedb.Actor{ID: principal, Kind: "LOCAL_USER", Name: "alice"},
		Now:         triageTime,
	}
}

func openTriageStore(t *testing.T) (string, *store.Store) {
	t.Helper()
	dir := stateDir(t)
	create(t, dir)
	return dir, open(t, dir)
}

func seedTriageItem(t *testing.T, s *store.Store, id string) statedb.TriageStored {
	t.Helper()
	got, err := statedb.InsertTriageItem(context.Background(), s, statedb.TriageSeed{
		ID: id, Kind: "CONDITION", Severity: "WARNING",
		Title: "agent reconnecting", Fingerprint: "fp-" + id,
		DetailKind: "note", DetailJSON: `{"text":"retrying"}`,
		Now: triageTime,
	})
	if err != nil {
		t.Fatalf("InsertTriageItem: %v", err)
	}
	if got.Version != 1 || got.State != "OPEN" || got.HubSeq == 0 {
		t.Fatalf("seeded item = version %d state %q hub_seq %d, want 1 OPEN stamped",
			got.Version, got.State, got.HubSeq)
	}
	return got
}

// TestTriageMigrationAddsItsTables proves migration 19 lands the hub-state
// triage tables on a fresh database.
func TestTriageMigrationAddsItsTables(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	create(t, dir)
	db := rawDB(t, dir)
	for _, table := range []string{"triage_items", "triage_request_log"} {
		var name string
		if err := db.QueryRowContext(t.Context(),
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name); err != nil {
			t.Errorf("table %s missing after migration: %v", table, err)
		}
	}
}

// TestSnoozeTriageItemMovesAnOpenItem proves the happy path: state
// SNOOZED, version 2, the Watch stamp moved, and one triage.snooze audit
// row naming the item and its new wake time.
func TestSnoozeTriageItemMovesAnOpenItem(t *testing.T) {
	t.Parallel()
	dir, s := openTriageStore(t)
	seed := seedTriageItem(t, s, "ti-1")
	until := triageTime.Add(2 * time.Hour)
	got, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-1", 'a'), ID: "ti-1",
		ExpectedVersion: seed.Version, Until: until,
	})
	if err != nil {
		t.Fatalf("SnoozeTriageItem: %v", err)
	}
	if got.Replay {
		t.Fatal("first snooze is not a replay")
	}
	if got.State != "SNOOZED" || got.Version != 2 || got.SnoozedUntilMs != until.UnixMilli() {
		t.Errorf("snoozed = state %q version %d until_ms %d, want SNOOZED 2 %d",
			got.State, got.Version, got.SnoozedUntilMs, until.UnixMilli())
	}
	if got.HubSeq <= seed.HubSeq {
		t.Errorf("hub_seq %d did not move past the insert stamp %d", got.HubSeq, seed.HubSeq)
	}
	rows := auditRows(t, dir)
	var found *auditRow
	for i, r := range rows {
		if r.Action == "triage.snooze" {
			found = &rows[i]
		}
	}
	if found == nil {
		t.Fatal("no triage.snooze audit row")
	}
	if found.Target != "triage/ti-1" || found.Request != "sn-1" {
		t.Errorf("audit row target %q request %q, want triage/ti-1 sn-1", found.Target, found.Request)
	}
}

// TestSnoozeTriageItemReplay proves an idempotent retry returns the stored
// item without bumping the version or writing again.
func TestSnoozeTriageItemReplay(t *testing.T) {
	t.Parallel()
	_, s := openTriageStore(t)
	seed := seedTriageItem(t, s, "ti-1")
	until := triageTime.Add(2 * time.Hour)
	first, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-1", 'a'), ID: "ti-1",
		ExpectedVersion: seed.Version, Until: until,
	})
	if err != nil {
		t.Fatalf("first snooze: %v", err)
	}
	second, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-1", 'a'), ID: "ti-1",
		ExpectedVersion: seed.Version, Until: until,
	})
	if err != nil {
		t.Fatalf("replay snooze: %v", err)
	}
	if !second.Replay {
		t.Error("same key and body is not marked a replay")
	}
	if second.Version != first.Version || second.HubSeq != first.HubSeq || second.State != "SNOOZED" {
		t.Errorf("replay = %+v, want the stored snooze %+v", second, first)
	}
	if _, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-1", 'b'), ID: "ti-1",
		ExpectedVersion: seed.Version, Until: until,
	}); !errors.Is(err, statedb.ErrRequestConflict) {
		t.Errorf("same key another body = %v, want ErrRequestConflict", err)
	}
}

// TestSnoozeTriageItemRefusals is the contract table: unknown ids,
// stale versions and closed items are refused, and nothing is written.
func TestSnoozeTriageItemRefusals(t *testing.T) {
	t.Parallel()
	dir, s := openTriageStore(t)
	seed := seedTriageItem(t, s, "ti-1")
	until := triageTime.Add(time.Hour)
	cases := []struct {
		name string
		in   statedb.TriageSnooze
		want error
	}{
		{"unknown", statedb.TriageSnooze{
			Write: triageWrite("local:alice", "u-1", 'a'), ID: "ti-missing",
			ExpectedVersion: 1, Until: until,
		}, statedb.ErrTriageUnknown},
		{"stale", statedb.TriageSnooze{
			Write: triageWrite("local:alice", "s-1", 'a'), ID: "ti-1",
			ExpectedVersion: seed.Version + 1, Until: until,
		}, &statedb.VersionStale{}},
		{"past wake time", statedb.TriageSnooze{
			Write: triageWrite("local:alice", "p-1", 'a'), ID: "ti-1",
			ExpectedVersion: seed.Version, Until: triageTime.Add(-time.Hour),
		}, statedb.ErrTriagePastUntil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := statedb.SnoozeTriageItem(context.Background(), s, tc.in)
			var stale *statedb.VersionStale
			switch {
			case errors.As(tc.want, &stale):
				var got *statedb.VersionStale
				if !errors.As(err, &got) {
					t.Fatalf("snooze = %v, want a VersionStale", err)
				}
				if got.Current != seed.Version {
					t.Fatalf("stale current = %d, want %d", got.Current, seed.Version)
				}
			default:
				if !errors.Is(err, tc.want) {
					t.Fatalf("snooze = %v, want %v", err, tc.want)
				}
			}
		})
	}
	before := len(auditRows(t, dir))
	if _, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "s-2", 'a'), ID: "ti-missing",
		ExpectedVersion: 1, Until: until,
	}); !errors.Is(err, statedb.ErrTriageUnknown) {
		t.Fatalf("unknown snooze = %v, want ErrTriageUnknown", err)
	}
	if got := len(auditRows(t, dir)); got != before {
		t.Errorf("a refused snooze wrote audit rows: %d -> %d", before, got)
	}
}

// TestDismissTriageItemClosesAnItem proves the happy path: state
// DISMISSED, the operator and time recorded, the wake time of the
// earlier snooze cleared, and one triage.dismiss audit row carrying
// the note.
func TestDismissTriageItemClosesAnItem(t *testing.T) {
	t.Parallel()
	dir, s := openTriageStore(t)
	seed := seedTriageItem(t, s, "ti-9")
	if _, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-0", 'a'), ID: "ti-9",
		ExpectedVersion: seed.Version, Until: triageTime.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	got, err := statedb.DismissTriageItem(context.Background(), s, statedb.TriageDismiss{
		Write: triageWrite("local:alice", "di-1", 'a'), ID: "ti-9",
		ExpectedVersion: seed.Version + 1, Note: "handled at the machine",
	})
	if err != nil {
		t.Fatalf("DismissTriageItem: %v", err)
	}
	if got.State != "DISMISSED" || got.Version != 3 {
		t.Errorf("dismissed = state %q version %d, want DISMISSED 3", got.State, got.Version)
	}
	if got.SnoozedUntilMs != 0 {
		t.Errorf("dismissed snoozed_until_ms = %d, want the wake time cleared", got.SnoozedUntilMs)
	}
	if got.ActedBy.ID != "local:alice" || got.ActedMs != triageTime.UnixMilli() {
		t.Errorf("acted by %+v at %d, want local:alice at %d",
			got.ActedBy, got.ActedMs, triageTime.UnixMilli())
	}
	if got.HubSeq <= seed.HubSeq {
		t.Errorf("hub_seq %d did not move past the insert stamp %d", got.HubSeq, seed.HubSeq)
	}
	var found *auditRow
	for i, r := range auditRows(t, dir) {
		if r.Action == "triage.dismiss" {
			found = &auditRows(t, dir)[i]
		}
	}
	if found == nil {
		t.Fatal("no triage.dismiss audit row")
	}
	if found.Target != "triage/ti-9" {
		t.Errorf("audit target %q, want triage/ti-9", found.Target)
	}
}

// TestTriageClosedItemsStayClosed proves terminal and in-flight items
// refuse both writes: a snooze of a dismissed item, a dismiss of a
// snoozed-then-dismissed item, and a dismiss naming a stale version.
func TestTriageClosedItemsStayClosed(t *testing.T) {
	t.Parallel()
	_, s := openTriageStore(t)
	seed := seedTriageItem(t, s, "ti-7")
	if _, err := statedb.DismissTriageItem(context.Background(), s, statedb.TriageDismiss{
		Write: triageWrite("local:alice", "di-1", 'a'), ID: "ti-7",
		ExpectedVersion: seed.Version,
	}); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if _, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-2", 'a'), ID: "ti-7",
		ExpectedVersion: seed.Version + 1, Until: triageTime.Add(time.Hour),
	}); !errors.Is(err, statedb.ErrTriageClosed) {
		t.Errorf("snooze of a dismissed item = %v, want ErrTriageClosed", err)
	}
	if _, err := statedb.DismissTriageItem(context.Background(), s, statedb.TriageDismiss{
		Write: triageWrite("local:alice", "di-2", 'a'), ID: "ti-7",
		ExpectedVersion: seed.Version + 1,
	}); !errors.Is(err, statedb.ErrTriageClosed) {
		t.Errorf("second dismiss = %v, want ErrTriageClosed", err)
	}
	if _, err := statedb.DismissTriageItem(context.Background(), s, statedb.TriageDismiss{
		Write: triageWrite("local:alice", "di-3", 'a'), ID: "ti-7",
		ExpectedVersion: seed.Version,
	}); !errors.Is(err, statedb.ErrTriageClosed) {
		t.Errorf("stale dismiss of a dismissed item = %v, want ErrTriageClosed (closed wins)", err)
	}
}

// TestSnoozeReplayAfterTheWakeTime proves the 24 h idempotency promise:
// a retry of a committed snooze returns the stored item even after its
// wake time passed, because the replay lookup runs before the
// future-time check.
func TestSnoozeReplayAfterTheWakeTime(t *testing.T) {
	t.Parallel()
	_, s := openTriageStore(t)
	seed := seedTriageItem(t, s, "ti-p")
	until := triageTime.Add(time.Hour)
	first, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-p", 'a'), ID: "ti-p",
		ExpectedVersion: seed.Version, Until: until,
	})
	if err != nil {
		t.Fatalf("first snooze: %v", err)
	}
	late := triageWrite("local:alice", "sn-p", 'a')
	late.Now = triageTime.Add(2 * time.Hour)
	second, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: late, ID: "ti-p", ExpectedVersion: seed.Version, Until: until,
	})
	if err != nil {
		t.Fatalf("late retry: %v, want the stored snooze", err)
	}
	if !second.Replay || second.Version != first.Version || second.SnoozedUntilMs != until.UnixMilli() {
		t.Errorf("late retry = %+v, want the stored snooze %+v", second, first)
	}
}

// TestTriagePrunesExpiredIdempotencyRows proves each write sweeps a
// bounded batch of expired request records: a snooze a day later leaves
// only its own key behind.
func TestTriagePrunesExpiredIdempotencyRows(t *testing.T) {
	t.Parallel()
	dir, s := openTriageStore(t)
	seedTriageItem(t, s, "ti-q1")
	seedTriageItem(t, s, "ti-q2")
	if _, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "old-key", 'a'), ID: "ti-q1",
		ExpectedVersion: 1, Until: triageTime.Add(time.Hour),
	}); err != nil {
		t.Fatalf("first snooze: %v", err)
	}
	next := triageWrite("local:alice", "new-key", 'a')
	next.Now = triageTime.Add(25 * time.Hour)
	if _, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: next, ID: "ti-q2", ExpectedVersion: 1, Until: next.Now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("later snooze: %v", err)
	}
	var keys []string
	rows, err := rawDB(t, dir).QueryContext(t.Context(),
		`SELECT request_id FROM triage_request_log ORDER BY request_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0] != "new-key" {
		t.Errorf("request log keys = %q, want only the live new-key", keys)
	}
}

// TestTriageSnoozeExtendsASnoozedItem proves a second snooze with a new
// key moves the wake time forward instead of refusing.
func TestTriageSnoozeExtendsASnoozedItem(t *testing.T) {
	t.Parallel()
	_, s := openTriageStore(t)
	seed := seedTriageItem(t, s, "ti-5")
	first, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-1", 'a'), ID: "ti-5",
		ExpectedVersion: seed.Version, Until: triageTime.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("first snooze: %v", err)
	}
	later := triageTime.Add(4 * time.Hour)
	second, err := statedb.SnoozeTriageItem(context.Background(), s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "sn-2", 'a'), ID: "ti-5",
		ExpectedVersion: first.Version, Until: later,
	})
	if err != nil {
		t.Fatalf("second snooze: %v", err)
	}
	if second.State != "SNOOZED" || second.Version != 3 || second.SnoozedUntilMs != later.UnixMilli() {
		t.Errorf("extended = state %q version %d until_ms %d, want SNOOZED 3 %d",
			second.State, second.Version, second.SnoozedUntilMs, later.UnixMilli())
	}
}
