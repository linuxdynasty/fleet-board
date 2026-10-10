package conn_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// 02i "Clocks": a command's deadline is hub time, so the loop prunes the
// command log by hub time (HelloAck.hub_time plus the time since), never by
// the agent's wall clock. Here the agent's clock runs 2 h ahead of the
// hub's. Past 10,000 entries, the oldest one's deadline is an hour after the
// hub's now but an hour before the agent's: the hub may still re-send it, so
// neither the prune before the Snapshot nor the one after a ResultAck may
// forget it.
func TestTheCommandLogIsPrunedByHubTime(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const skew = 2 * time.Hour
	h := startConnectHub(t)
	h.set(func(h *connectHub) {
		h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.HubTime = timestamppb.New(time.Now().Add(-skew)) }
	})
	a := enrolled(t, h)
	db := a.openDB(t)
	now := time.Now()
	logCommands(t, db, 0, ring.MaxCommands+1, now, func(i int, c *ring.Command) {
		c.NotAfter = now.Add(-skew - time.Minute) // past in both clocks
		if i == 0 {
			c.NotAfter = now.Add(-time.Hour) // inside it by the hub's clock only
		}
	})
	kept := func(i int) bool {
		_, err := db.Command(ctx, fmt.Sprintf("c-%05d", i))
		return err == nil
	}

	startLoop(t, a, nil)
	h.connected(t, 1)
	if !kept(0) || kept(1) {
		t.Fatalf("after the Snapshot's prune: c-00000 kept %v, c-00001 kept %v; want true, false", kept(0), kept(1))
	}

	logCommands(t, db, ring.MaxCommands+1, 1, now, func(_ int, c *ring.Command) {
		c.NotAfter = now.Add(-skew - time.Minute)
	})
	h.push(t, resultAck("c-unknown"))
	waitFor(t, 10*time.Second, "the prune after the ResultAck", func() bool { return !kept(0) || !kept(2) })
	if !kept(0) {
		t.Error("the prune after a ResultAck forgot c-00000, inside its deadline by the hub's clock")
	}
}

// logCommands logs n commands c-<from> to c-<from+n-1>, received an hour
// before now, 1 ms apart, after set adjusts each. Many writers at once, so
// the store's one writer batches them.
func logCommands(t *testing.T, db *ring.DB, from, n int, now time.Time, set func(i int, c *ring.Command)) {
	t.Helper()
	ctx := context.Background()
	var wg sync.WaitGroup
	next := make(chan int)
	for range 100 {
		wg.Go(func() {
			for i := range next {
				c := ring.Command{
					ID: fmt.Sprintf("c-%05d", i), Kind: "capture_pane", Epoch: 1, Seq: uint64(i + 1), //nolint:gosec // G115: i is below 10,003
					Received: now.Add(-time.Hour + time.Duration(i)*time.Millisecond),
				}
				set(i, &c)
				if _, _, err := db.LogCommand(ctx, c); err != nil {
					t.Error(err)
				}
			}
		})
	}
	for i := from; i < from+n; i++ {
		next <- i
	}
	close(next)
	wg.Wait()
}

// A HelloAck with no hub_time leaves the agent's own clock as hub time and
// skew 0; connect.hello says so (hub_time_ok=false) once per stream, so a
// zero skew is not mistaken for a measured one.
func TestAHelloAckWithNoHubTimeIsLogged(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{true, false} {
		h := startConnectHub(t)
		if missing {
			h.set(func(h *connectHub) {
				h.tamperAck = func(_ int, ack *agentv1.HelloAck) { ack.HubTime = nil }
			})
		}
		a := enrolled(t, h)
		r := startLoop(t, a, nil)
		h.connected(t, 1)
		r.connected(t)
		hello := a.events(t, "connect.hello")
		if len(hello) != 1 || hello[0]["hub_time_ok"] != !missing || (missing && hello[0]["skew_ms"] != float64(0)) {
			t.Errorf("hub_time missing %v: connect.hello = %v, want hub_time_ok %v", missing, hello, !missing)
		}
	}
}
