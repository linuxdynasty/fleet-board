package hookreconcile

import (
	"context"
	"database/sql"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// DefaultResendAfter is the reconcile resend throttle (03h, and one
// GOAL_FILE_NOT_SEEN per machine and harness per day): a state-based
// want with an in-flight send inside this window waits.
const DefaultResendAfter = 24 * time.Hour

// Applier is the snapshot reconcile pass (ingest.Applier, wired in
// daemon like observed.Applier): after every snapshot it compares the
// agent-reported hook states with the watched repos and enqueues
// EnsureGoalHooks per (harness, profile, enabled checkout) that is
// missing, unverified or behind Expected. It never runs for a harness
// with a live goal, and ESHU checkouts only report. Delivery needs no
// notify: the gateway's Ready kick after the snapshot commit sends what
// is pending. Expected versions come from lane 2's hook definitions;
// empty Expected means no version gate.
type Applier struct {
	Now         func() time.Time
	Expected    map[string]string
	ResendAfter time.Duration
}

func (a Applier) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a Applier) window() time.Duration {
	if a.ResendAfter <= 0 {
		return DefaultResendAfter
	}
	return a.ResendAfter
}

// Apply runs the pass inside the snapshot's writer job and returns no
// goal rows (it writes commands, not goals). A SQL failure rolls the
// snapshot back like any applier error; an unparsable stored row is the
// owning row's corruption and equally loud.
func (a Applier) Apply(ctx context.Context, tx *sql.Tx, machineID string, snap *agentv1.Snapshot) ([]statedb.GoalRow, error) {
	now := a.now()
	stored, err := statedb.ListHookRepos(ctx, tx)
	if err != nil {
		return nil, err
	}
	repos := make([]Repo, 0, len(stored))
	for _, r := range stored {
		repos = append(repos, Repo{Host: r.Host, Path: r.Path, Enabled: r.Enabled, Mode: r.Mode})
	}
	live, err := statedb.LiveGoalHarnesses(ctx, tx, machineID)
	if err != nil {
		return nil, err
	}
	window := a.window()
	recent, err := outbox.RecentCommands(ctx, tx, machineID, "ensure_goal_hooks", now.Add(-window))
	if err != nil {
		return nil, err
	}
	in := SnapshotInput(snap, repos, a.Expected, sentKeys(recent, a.Expected), now, window)
	// The hub's own live goals guard too: a stale registry entry is not
	// the only way a goal is live in a pane.
	for h := range live {
		in.Live[h] = true
	}
	for _, w := range Decide(in) {
		cmd := CommandFor(w, a.Expected[w.Key.Harness])
		if _, err := outbox.Enqueue(ctx, tx, outbox.In{MachineID: machineID, Command: cmd, Now: now}); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// sentKeys folds recent sends into the throttle map: only in-flight
// (pending, delivered) sends for the currently expected version count,
// latest send wins per key.
func sentKeys(recent []outbox.StoredCommand, expected map[string]string) map[Key]time.Time {
	sent := map[Key]time.Time{}
	for _, sc := range recent {
		if sc.State != "pending" && sc.State != "delivered" {
			continue
		}
		in := sc.Command.GetEnsureGoalHooks()
		if in == nil {
			continue
		}
		if want, ok := expected[in.GetHarness().GetName()]; ok && want != "" && in.GetExpectedVersion() != want {
			continue
		}
		key := Key{
			Harness: in.GetHarness().GetName(), Profile: in.GetProfile(),
			RepoHost: in.GetWorkdir().GetRepo().GetHost(), RepoPath: in.GetWorkdir().GetRepo().GetPath(),
		}
		if at := sc.Created; at.After(sent[key]) {
			sent[key] = at
		}
	}
	return sent
}
