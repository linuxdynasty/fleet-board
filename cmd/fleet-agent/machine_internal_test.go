package main

import (
	"context"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// run wires the unacked count and the reconnect count into the heartbeat's
// health; the sampler's own RSS reading arrives with the first sample.
func TestMachineHooksFillHealthFromTheSeams(t *testing.T) {
	t.Parallel()
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "boot"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	seams := healthSeams{
		Unacked:     func() int { return 5 },
		Reconnects:  func() uint64 { return 9 },
		Refused:     func() uint64 { return 3 },
		ControlDown: func() uint32 { return 2 },
	}
	_, heartbeat, err := machineHooks(ctx, &wg, tel, t.TempDir(), nil, seams, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		hb := &agentv1.Heartbeat{}
		heartbeat(ctx, hb)
		h := hb.GetHealth()
		if h.GetUnackedResults() != 5 || h.GetReconnectsTotal() != 9 ||
			h.GetCommandsRefusedTotal() != 3 || h.GetTmuxControlDown() != 2 {
			t.Fatalf("health = %v, want unacked 5, reconnects 9, refused 3 and control down 2", h)
		}
		if h.RssBytes != nil { // the first sample has arrived
			if h.GetRssBytes() < 1<<20 {
				t.Fatalf("rss_bytes = %d, want this process's resident memory", h.GetRssBytes())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no rss_bytes after 30 s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
