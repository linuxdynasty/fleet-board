package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
	writePolicy(t, s.cfg, "schema_version: \"1\"\nallowed_repo_roots: []\n")
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
		t.Errorf("events seen %v, want a SHED resource_sample and a NEVER_DROP policy_changed", kinds)
	}
}

// The whole run: events.New, the Emitter in the loop's options and the
// policy seam. A policy change while connected reaches the real hub as an
// event it commits (hub_last_event_seq rises past the snapshot).
func TestARunSendsAPolicyChangeToTheHub(t *testing.T) {
	t.Parallel()
	s := newStartup(t)
	writePolicy(t, s.cfg, "schema_version: \"1\"\n")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	defer func() { cancel(); <-done }()
	waitTrue(t, 20*time.Second, "ONLINE", func() bool { return s.hub.Machine(t, s.machine).State == "ONLINE" })
	snap := s.hub.Machine(t, s.machine).SnapshotSeq
	writePolicy(t, s.cfg, "schema_version: \"1\"\nallowed_repo_roots: []\n")
	waitTrue(t, 20*time.Second, "the hub to commit the policy_changed event", func() bool {
		return s.hub.Machine(t, s.machine).HubLastEventSeq > snap
	})
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
