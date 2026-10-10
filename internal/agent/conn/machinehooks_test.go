package conn_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
)

// The machine package's hooks, run by the real loop: the Snapshot the hub
// receives carries the loop's own facts (hostname, boot_id) with the B1 facts
// added, and a B2 sample; the Heartbeat carries load1.
func TestMachineHooksFillTheSnapshotAndHeartbeatTheHubReceives(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	h.set(heartbeatEvery(durationpb.New(time.Second)))

	sampler, err := machine.NewSampler(machine.SamplerOptions{StateDir: a.dir})
	if err != nil {
		t.Fatal(err)
	}
	collector, err := machine.NewCollector(t.Context(), machine.CollectorOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sub, unsub := sampler.Subscribe()
	defer unsub()
	go sampler.Run(t.Context())
	<-sub // the first sample is in before the loop's first snapshot
	hooks := machine.Hooks{Collector: collector, Sampler: sampler}
	startLoop(t, a, func(o *conn.LoopOptions) {
		o.Snapshot, o.Heartbeat = hooks.Snapshot, hooks.Heartbeat
	})
	waitFor(t, 15*time.Second, "a heartbeat", func() bool { return h.count() >= 1 && len(h.heartbeats(1)) >= 1 })

	snap := h.stream(1).msgs[0].GetSnapshot()
	f := snap.GetFacts()
	if f.GetBootId() == "" || f.GetHostname() == "" || f.GetOs() == "" {
		t.Errorf("the loop's facts were lost: %v", f)
	}
	if f.GetOsVersion() == "" || f.GetRamBytes() == 0 || f.GetAgentBinaryHash() == "" || f.GetTimezone() == "" {
		t.Errorf("B1 facts missing from the snapshot: %v", f)
	}
	r := snap.GetResources()
	if r == nil || r.MemTotalBytes == nil || r.Load1 == nil || len(r.GetDisk()) != 1 || r.GetDisk()[0].GetPath() != a.dir {
		t.Errorf("B2 resources missing from the snapshot: %v", r)
	}
	if hb := h.heartbeats(1)[0].hb; hb.Load1 == nil {
		t.Errorf("heartbeat load1 unset: %v", hb)
	}
}
