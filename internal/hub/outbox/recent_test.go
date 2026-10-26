package outbox_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

func recent(t *testing.T, s interface {
	Read(context.Context, func(*sql.Tx) error) error
}, machine, kind string, since time.Time,
) ([]outbox.StoredCommand, error) {
	t.Helper()
	var got []outbox.StoredCommand
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		var err error
		got, err = outbox.RecentCommands(context.Background(), tx, machine, kind, since)
		return err
	})
	return got, err
}

func hookCommand(harness, host, path, version string) *agentv1.Command {
	return &agentv1.Command{Kind: &agentv1.Command_EnsureGoalHooks{
		EnsureGoalHooks: &agentv1.EnsureGoalHooks{
			Harness:         &typev1.Harness{Name: harness},
			Workdir:         &typev1.Workdir{Repo: &typev1.RepoRef{Host: host, Path: path}},
			ExpectedVersion: version,
		},
	}}
}

// TestRecentCommandsListsKindSince: the hook reconcile throttle reads
// back recent sends of one kind with their states.
func TestRecentCommandsListsKindSince(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	old := t0.Add(-48 * time.Hour)
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: hookCommand("codex", "github.com", "acme/web", "v3"), Now: old}); err != nil {
		t.Fatal(err)
	}
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: hookCommand("codex", "github.com", "acme/web", "v3"), Now: t0}); err != nil {
		t.Fatal(err)
	}
	if _, err := enqueue(t, s, outbox.In{MachineID: "m1", Command: interrupt("c9"), Now: t0}); err != nil {
		t.Fatal(err)
	}
	got, err := recent(t, s, "m1", "ensure_goal_hooks", t0.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("recent = %d rows, want 1", len(got))
	}
	if got[0].Command.GetEnsureGoalHooks().GetHarness().GetName() != "codex" || got[0].State != "pending" {
		t.Fatalf("recent[0] = %+v", got[0])
	}
	if _, err := recent(t, s, "nope", "ensure_goal_hooks", t0.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
}
