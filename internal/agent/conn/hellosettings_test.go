package conn_test

import (
	"context"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// Hello reports the settings the agent holds, read from agent.db, so the
// hub sends Settings only when its own are newer or of another epoch (03e
// "Generation").
func TestHelloCarriesTheHeldSettings(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	if err := a.openDB(t).SaveSettings(context.Background(), ring.Held{
		Epoch: "epoch-7", Generation: 13, Settings: []byte{}, Ack: []byte{},
	}); err != nil {
		t.Fatal(err)
	}
	r := startLoop(t, a, nil)
	h.connected(t, 1)
	r.connected(t)
	hello := h.stream(1).hello
	if hello.GetSettingsEpoch() != "epoch-7" || hello.GetSettingsGeneration() != 13 {
		t.Errorf("Hello settings (%q, %d), want (epoch-7, 13)", hello.GetSettingsEpoch(), hello.GetSettingsGeneration())
	}
	r.running(t)
}
