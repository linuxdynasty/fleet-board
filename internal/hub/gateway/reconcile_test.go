package gateway_test

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// openWindow sets restore_reconcile_until to until (fleetd restore's step
// 4, 02g), keeping the settings epoch the store has.
func openWindow(t *testing.T, h *hub, until time.Time) {
	t.Helper()
	err := statedb.ApplyRestore(context.Background(), h.store, statedb.RestoreUpdate{
		SettingsEpoch: h.id.SettingsEpoch, ReconcileUntil: until,
	})
	if err != nil {
		t.Fatalf("ApplyRestore: %v", err)
	}
}

// The first Hello inside the restore's window is answered with
// SnapshotRequest{reconcile} after HelloAck; the next Hello is not (02g,
// 03f "Restore").
func TestFirstHelloInTheWindowGetsASnapshotRequest(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	openWindow(t, h, h.clock.now().Add(time.Hour))
	first := h.open(t, m)
	first.send(t, hello(m, 0))
	first.ack(t)
	if r := first.next(t).GetSnapshotRequest(); r.GetReason() != "reconcile" {
		t.Fatalf("after HelloAck: %v, want SnapshotRequest{reconcile}", r)
	}
	h.clock.set(h.clock.now().Add(6 * time.Second)) // past the Hello rate limit
	second := h.open(t, m)
	second.send(t, hello(m, 1))
	second.ack(t)
	second.silent(t, 200*time.Millisecond)
}

// A machine the restored store does not know is adopted inside the window:
// its Hello is accepted (no Bye), its row is recreated, and the adoption
// is audited, logged and counted (02g).
func TestAnUnknownMachineIsAdoptedInsideTheWindow(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	h.exec(t, `DELETE FROM enroll_responses WHERE machine_id = ?`, m.id)
	h.exec(t, `DELETE FROM machines WHERE machine_id = ?`, m.id)
	openWindow(t, h, h.clock.now().Add(time.Hour))
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	if r := s.next(t).GetSnapshotRequest(); r.GetReason() != "reconcile" {
		t.Fatalf("after HelloAck: %v, want SnapshotRequest{reconcile}", r)
	}
	rows := h.auditRows(t, "machine.adopted")
	if len(rows) != 1 || rows[0].target != "machines/"+m.id || rows[0].machine != m.id {
		t.Errorf("machine.adopted rows = %+v, want one for %s", rows, m.id)
	}
	lines := h.events(t, "connect.adopted")
	if len(lines) != 1 || lines[0]["machine_id"] != m.id {
		t.Errorf("connect.adopted lines = %v, want one for %s", lines, m.id)
	}
	if v := sample(scrape(t, h.fixture), "fleet_hub_connect_adopted_total", `machine="`+m.id+`"`); v != 1 {
		t.Errorf("fleet_hub_connect_adopted_total{machine=%s} = %v, want 1", m.id, v)
	}
	if n := h.serialRows(t); n != 1 {
		t.Errorf("%d cert_serials rows after adoption, want 1", n)
	}
}

// An unknown machine past the window is still refused with
// Bye{UNKNOWN_MACHINE}.
func TestAnUnknownMachinePastTheWindowIsRefused(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	h.exec(t, `DELETE FROM enroll_responses WHERE machine_id = ?`, m.id)
	h.exec(t, `DELETE FROM machines WHERE machine_id = ?`, m.id)
	openWindow(t, h, h.clock.now().Add(-time.Hour))
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	if r := s.bye(t); r != agentv1.ByeReason_BYE_REASON_UNKNOWN_MACHINE {
		t.Errorf("Bye reason = %v, want UNKNOWN_MACHINE", r)
	}
	if n := len(h.auditRows(t, "machine.adopted")); n != 0 {
		t.Errorf("%d machine.adopted rows past the window, want 0", n)
	}
}
