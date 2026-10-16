package statedb_test

import (
	"context"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// TestOwnerWritesPublishWatchOrder proves the Watch substrate of every
// 13a write on the REPOS, ACCOUNTS, SETTINGS and TRIAGE topics: each
// write stamps its resource row and leaves its audit row, and the audit
// hub_seq order matches the commit order, so a Watch reader sees every
// change once, in order (02c "Order").
func TestOwnerWritesPublishWatchOrder(t *testing.T) {
	t.Parallel()
	dir, s := openTriageStore(t)
	ctx := context.Background()
	now := triageTime

	repo := createRepo(t, s, repoWrite("local:alice", "w-repo", 'a'))
	account := createOwnerAccount(t, s, configWrite("local:alice", "w-account", 'a'))
	settings, err := statedb.UpdateFleetSettings(ctx, s, statedb.UpdateSettingsOp{
		ConfigWrite: configWrite("local:alice", "w-settings", 'a'),
		Merge:       fleetMerge([]byte(`{\"hub\":{}}`)),
	})
	if err != nil {
		t.Fatalf("UpdateFleetSettings: %v", err)
	}
	seedA := seedTriageItem(t, s, "ti-a")
	seedB := seedTriageItem(t, s, "ti-b")
	snoozed, err := statedb.SnoozeTriageItem(ctx, s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "w-snooze", 'a'), ID: "ti-a",
		ExpectedVersion: seedA.Version, Until: now.AddDate(0, 0, 1),
	})
	if err != nil {
		t.Fatalf("SnoozeTriageItem: %v", err)
	}
	dismissed, err := statedb.DismissTriageItem(ctx, s, statedb.TriageDismiss{
		Write: triageWrite("local:alice", "w-dismiss", 'a'), ID: "ti-b",
		ExpectedVersion: seedB.Version, Note: "done",
	})
	if err != nil {
		t.Fatalf("DismissTriageItem: %v", err)
	}
	for name, seq := range map[string]int64{
		"repo": repo.HubSeq, "account": account.HubSeq, "settings": settings.HubSeq,
		"snoozed": snoozed.HubSeq, "dismissed": dismissed.HubSeq,
	} {
		if seq <= 0 {
			t.Errorf("%s hub_seq = %d, want stamped", name, seq)
		}
	}
	rows := auditRows(t, dir)
	seqOf := map[string]int64{}
	for _, r := range rows {
		switch r.Action {
		case "repo.create", "account.create", "settings.update", "triage.snooze", "triage.dismiss":
			if _, dup := seqOf[r.Action]; dup {
				t.Fatalf("action %s audited twice", r.Action)
			}
			seqOf[r.Action] = r.HubSeq
		}
	}
	want := []string{"repo.create", "account.create", "settings.update", "triage.snooze", "triage.dismiss"}
	for _, a := range want {
		if _, ok := seqOf[a]; !ok {
			t.Fatalf("no audit row for %s", a)
		}
	}
	for i := 1; i < len(want); i++ {
		if seqOf[want[i]] <= seqOf[want[i-1]] {
			t.Errorf("audit hub_seq %s=%d not after %s=%d: Watch would reorder them",
				want[i], seqOf[want[i]], want[i-1], seqOf[want[i-1]])
		}
	}
}

// TestTriageReplayTakesNoNewStamp proves an idempotent triage retry
// publishes nothing new to Watch: the stored stamp comes back and no
// audit row is added.
func TestTriageReplayTakesNoNewStamp(t *testing.T) {
	t.Parallel()
	dir, s := openTriageStore(t)
	ctx := context.Background()
	seed := seedTriageItem(t, s, "ti-r")
	first, err := statedb.SnoozeTriageItem(ctx, s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "w-1", 'a'), ID: "ti-r",
		ExpectedVersion: seed.Version, Until: triageTime.AddDate(0, 0, 1),
	})
	if err != nil {
		t.Fatalf("first snooze: %v", err)
	}
	before := len(auditRows(t, dir))
	second, err := statedb.SnoozeTriageItem(ctx, s, statedb.TriageSnooze{
		Write: triageWrite("local:alice", "w-1", 'a'), ID: "ti-r",
		ExpectedVersion: seed.Version, Until: triageTime.AddDate(0, 0, 1),
	})
	if err != nil {
		t.Fatalf("replay snooze: %v", err)
	}
	if !second.Replay || second.HubSeq != first.HubSeq {
		t.Errorf("replay stamp = %d replay=%v, want %d true", second.HubSeq, second.Replay, first.HubSeq)
	}
	if got := len(auditRows(t, dir)); got != before {
		t.Errorf("a replay wrote audit rows: %d -> %d", before, got)
	}
}
