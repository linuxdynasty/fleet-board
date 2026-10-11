package conn_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// The agent's real loop sends its Snapshot (the loop's own facts, plus a
// policy and a resource sample from the Snapshot hook) and the real hub
// stores them and marks the machine ONLINE. After a hub restart the next
// connection's Snapshot replaces them on epoch 2; resources the agent
// could not read stay unknown.
func TestTheRealHubAppliesTheLoopsSnapshot(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	var builds atomic.Int32
	_, r, id := realHubAgent(t, h, func(o *conn.LoopOptions) {
		o.Snapshot = func(_ context.Context, s *agentv1.Snapshot) error {
			if builds.Add(1) == 1 {
				load := 0.25
				s.Resources = &agentv1.ResourceSample{Load1: &load}
				s.Policy = &agentv1.PolicyInfo{Hash: "hash-1"}
				return nil
			}
			s.Policy = &agentv1.PolicyInfo{Hash: "hash-2", SafeMode: true, Error: "line 1: unknown key"}
			return nil
		}
	})
	r.connected(t)
	waitFor(t, 10*time.Second, "ONLINE", func() bool { return h.Machine(t, id).State == "ONLINE" })
	m := h.Machine(t, id)
	if m.SnapshotEpoch != 1 || m.SnapshotSeq == 0 || m.EventGeneration != 1 || m.Policy.Hash != "hash-1" ||
		!strings.Contains(m.Facts, `"agentVersion":"test"`) || !strings.Contains(m.Resources, `"load1":0.25`) {
		t.Errorf("machine %+v, want the first snapshot on epoch 1", m)
	}
	first := m.SnapshotSeq
	// The hub logs snapshot.applied just after the commit that made the
	// machine ONLINE, so the line can trail the state by a moment.
	waitFor(t, 10*time.Second, "one snapshot.applied line", func() bool { return len(h.Events(t, "snapshot.applied")) >= 1 })
	if n := len(h.Events(t, "snapshot.applied")); n != 1 {
		t.Errorf("%d snapshot.applied lines, want 1", n)
	}
	h.Restart(t)
	waitFor(t, 10*time.Second, "the second snapshot", func() bool { return h.Machine(t, id).SnapshotEpoch == 2 })
	m = h.Machine(t, id)
	if m.State != "ONLINE" || m.Policy.Hash != "hash-2" || !m.Policy.SafeMode || m.Resources != "" ||
		m.SnapshotSeq <= first || m.EventGeneration != 1 {
		t.Errorf("machine %+v, want the second snapshot's policy, unknown resources, a higher seq, no rollback", m)
	}
	r.running(t)
}
