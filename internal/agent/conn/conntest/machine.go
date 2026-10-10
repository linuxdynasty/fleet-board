package conntest

import (
	"context"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Machine reads what the hub's Hello and Snapshot transactions stored for
// machineID (machines, through statedb.ReadSnapshot; plan row 5 PR 5c1).
func (h *Hub) Machine(t *testing.T, machineID string) statedb.MachineSnapshot {
	t.Helper()
	m, err := statedb.ReadSnapshot(context.Background(), h.current(), machineID)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
