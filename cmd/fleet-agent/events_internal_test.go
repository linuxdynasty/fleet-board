package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// writePolicy replaces policy.yaml by a rename, as an editor does.
func writePolicy(t *testing.T, cfg, text string) {
	t.Helper()
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(cfg, ".policy.tmp")
	if err := os.WriteFile(tmp, []byte(text), 0o644); err != nil { //nolint:gosec // G306: the policy file is 0644 by design
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(cfg, policy.File)); err != nil {
		t.Fatal(err)
	}
}

// Review P2 (7a-B): the run's wiring with a REAL emitter. machineHooks
// subscribes the emitter to the sampler, so the first sample is a SHED
// resource_sample on an attached stream's queue; startPolicy hands the
// policy loader events.PolicyChanges, so a policy file change is a
// policy_changed in the ring.
func TestTheRunWiresSamplesAndPolicyChangesIntoTheEmitter(t *testing.T) {
	t.Parallel()
	s := newStartup(t)
	db, err := ring.Open(t.Context(), s.dir, ring.Options{Telemetry: s.tel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	em, err := events.New(t.Context(), events.Options{DB: db, Telemetry: s.tel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(em.Close)
	c, err := em.Attach(t.Context(), 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Detach()
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	if _, _, err := machineHooks(ctx, &wg, s.tel, s.dir, nil, healthSeams{}, em); err != nil {
		t.Fatal(err)
	}
	writePolicy(t, s.cfg, "schema_version: \"1\"\n")
	pol, err := startPolicy(ctx, s.cfg, s.dir, s.machine, s.tel, events.PolicyChanges(em))
	if err != nil {
		t.Fatal(err)
	}
	defer pol.close()
	waitTrue(t, 20*time.Second, "policy watcher startup", func() bool {
		return pol.mgr.Watching() || pol.mgr.WatchOff()
	})
	watchOff := pol.mgr.WatchOff()
	writePolicy(t, s.cfg, "schema_version: \"1\"\nallowed_repo_roots: []\n")
	if watchOff {
		if err := pol.mgr.Snapshot(ctx, &agentv1.Snapshot{}); err != nil {
			t.Fatal(err)
		}
	}
	kinds := map[string]agentv1.DeliveryClass{}
	end := time.Now().Add(30 * time.Second)
	for len(kinds) < 2 && time.Now().Before(end) {
		evs, _, err := c.Next(t.Context(), 16)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			switch {
			case ev.GetResourceSample() != nil:
				kinds["resource_sample"] = ev.GetDelivery()
			case ev.GetPolicyChanged() != nil:
				kinds["policy_changed"] = ev.GetDelivery()
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if kinds["resource_sample"] != agentv1.DeliveryClass_DELIVERY_CLASS_SHED ||
		kinds["policy_changed"] != agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP {
		t.Errorf("events seen %v (watch off at startup: %v), want a SHED resource_sample and a NEVER_DROP policy_changed", kinds, watchOff)
	}
}

// Reconnecting builds a Snapshot even when policy watch setup failed. The
// agent keeps its loaded policy and sends the detected change to the real hub.
func TestARunSendsAPolicyChangeAfterHubReconnect(t *testing.T) {
	t.Parallel()
	s := newStartup(t)
	original := "schema_version: \"1\"\n"
	edited := original + "allowed_repo_roots: []\n"
	writePolicy(t, s.cfg, original)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	defer func() { cancel(); <-done }()
	waitTrue(t, 20*time.Second, "ONLINE", func() bool { return s.hub.Machine(t, s.machine).State == "ONLINE" })
	before := s.hub.Machine(t, s.machine)
	writePolicy(t, s.cfg, edited)
	s.hub.Restart(t)
	waitTrue(t, 20*time.Second, "the reconnect Snapshot", func() bool {
		now := s.hub.Machine(t, s.machine)
		return now.State == "ONLINE" && now.SnapshotEpoch > before.SnapshotEpoch
	})
	var committed []*agentv1.Event
	waitTrue(t, 20*time.Second, "the committed policy_changed event", func() bool {
		committed = committedPolicyEvents(t, s)
		return len(committed) > 0
	})
	if len(committed) != 1 {
		t.Fatalf("policy events = %d, want one", len(committed))
	}
	event := committed[0]
	changed := event.GetPolicyChanged()
	if event.GetDelivery() != agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP || changed == nil || changed.GetPreviousHash() != policyTextHash(original) || changed.GetPolicy().GetHash() != policyTextHash(edited) || !changed.GetPolicy().GetChangedSinceLoad() {
		t.Fatalf("committed policy event = %v", event)
	}
	snapshot := s.hub.Machine(t, s.machine)
	if snapshot.Policy.Hash != policyTextHash(original) || !snapshot.Policy.ChangedSinceLoad {
		t.Fatalf("reconnect replaced the loaded policy: %+v", snapshot.Policy)
	}
}

func policyTextHash(text string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(text))) }

func committedPolicyEvents(t *testing.T, s *startup) []*agentv1.Event {
	t.Helper()
	var out []*agentv1.Event
	// CurrentStore is obtained after Restart; a pre-restart handle is closed.
	err := s.hub.CurrentStore().Read(t.Context(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(t.Context(), `SELECT delivery, body FROM events WHERE machine_id = ? AND kind = 'policy_changed' ORDER BY seq`, s.machine)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var delivery string
			var body []byte
			if err := rows.Scan(&delivery, &body); err != nil {
				return err
			}
			if delivery != "never_drop" {
				return fmt.Errorf("policy event delivery = %q", delivery)
			}
			event := &agentv1.Event{}
			if err := proto.Unmarshal(body, event); err != nil {
				return err
			}
			out = append(out, event)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func waitTrue(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(limit)
	for !cond() {
		if time.Now().After(end) {
			t.Fatalf("timed out after %v waiting for %s", limit, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
