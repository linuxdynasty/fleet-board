package inventory_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// With the real emitter on an agent.db the lifecycle events are NEVER_DROP
// entries in the ring, in seq order, and a cursor reads them back.
func TestEventsLandInTheRing(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) { eachEventsLandInTheRing(t, b) })
	}
}

func eachEventsLandInTheRing(t *testing.T, b tmuxtest.Binary) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fleet-agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := ring.Open(t.Context(), dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "b", LogOutput: &logBuf{}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := events.New(t.Context(), events.Options{DB: db, Telemetry: tel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close(); _ = db.Close(); _ = tel.Shutdown(context.Background()) })

	folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
	dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
	r := startObserver(t, b, folder.Dir, func(o *inventory.Options) { o.Emitter = e }, "default")
	r.waitState(t, "default", tmux.StateUp)
	r.snapshot(t)
	dev.Run(t, "new-window", "-d", "-t", "work:", "exec cat")

	c, err := e.Attach(t.Context(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []*agentv1.Event
	deadline := time.Now().Add(wait)
	for len(got) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("ring holds %v, want a window CREATED and a PaneCreated", got)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		select {
		case <-e.Ready():
		case <-ctx.Done():
		}
		cancel()
		evs, _, err := c.Next(t.Context(), 64)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, evs...)
	}
	if got[0].GetWindow().GetKind() != agentv1.ChangeKind_CHANGE_KIND_CREATED || got[1].GetPaneCreated() == nil {
		t.Fatalf("ring order = %v", got)
	}
	for i, ev := range got {
		if ev.GetDelivery() != agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP || ev.GetSeq() == 0 || (i > 0 && ev.GetSeq() <= got[i-1].GetSeq()) {
			t.Fatalf("event %d = seq %d delivery %v", i, ev.GetSeq(), ev.GetDelivery())
		}
	}
}
