package gateway_test

import (
	"bytes"
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Arbiter ruling (round 2, A6): an Event of a body arm this hub has no
// name for, stamped NEVER_DROP, is stored with that field byte for byte,
// kind "unknown", and moves hub_last_event_seq.
func TestAnEventOfAnUnknownArmIsKeptByteForByte(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m, s := h.snapshotted(t, 10)
	field := protowire.AppendBytes(protowire.AppendTag(nil, 99, protowire.BytesType), []byte("a newer kind"))
	ev := &agentv1.Event{Seq: 11, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP}
	ev.ProtoReflect().SetUnknown(field)
	s.send(t, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{Event: ev}})
	h.waitSeq(t, m.id, 11)
	rows := h.eventRows(t, m.id)
	if len(rows) != 1 || rows[0].kind != "unknown" || !bytes.Contains(rows[0].body, field) {
		t.Fatalf("rows %+v, want one of kind unknown holding field 99", rows)
	}
	if got := decode(t, rows[0].body).ProtoReflect().GetUnknown(); !bytes.Equal(got, field) {
		t.Errorf("stored unknown field %x, want %x", got, field)
	}
}

// A live COALESCE event writes no row but moves hub_last_event_seq in the
// same job its applier runs in; an event at or below hub_last_event_seq is
// known: no row and no applier call.
func TestALiveCoalesceMovesTheSeqAndAKnownEventCallsNoApplier(t *testing.T) {
	t.Parallel()
	var (
		mu    sync.Mutex
		calls []statedb.EventOut
	)
	rec := eventApplierFunc(func(ctx context.Context, tx *sql.Tx, id string, out statedb.EventOut, _ *agentv1.Event) ([]statedb.GoalRow, []statedb.WorkItemRow, error) {
		var seq int64
		if err := tx.QueryRowContext(ctx, `SELECT hub_last_event_seq FROM machines WHERE machine_id = ?`, id).Scan(&seq); err != nil {
			return nil, nil, err
		}
		if uint64(seq) != out.HubLastEventSeq { //nolint:gosec // G115: CHECK >= 0
			t.Errorf("applier saw hub_last_event_seq %d in its tx, want %d (the same job)", seq, out.HubLastEventSeq)
		}
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, out)
		return nil, nil, nil
	})
	h := startHub(t, func(o *gateway.ServerOptions) { o.EventAppliers = []ingest.EventApplier{rec} })
	m, s := h.snapshotted(t, 10)
	s.send(t, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Seq: 12, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_COALESCE,
		Body: &agentv1.Event_PaneInput{PaneInput: &agentv1.PaneInput{}},
	}}})
	h.waitSeq(t, m.id, 12)
	if rows := h.eventRows(t, m.id); len(rows) != 0 {
		t.Errorf("%d rows for a live COALESCE event, want 0", len(rows))
	}
	h.clock.set(h.clock.now().Add(6 * time.Second))
	again := h.open(t, m)
	hi := hello(m, 1)
	hi.GetHello().LastEventSeq = 12
	again.send(t, hi)
	again.ack(t)
	again.send(t, snapshotMsg(13, "laptop", "h1"))
	h.waitRow(t, m.id, "the second snapshot", func(r machineRow) bool { return r.snapEpoch == 2 })
	again.send(t, eventMsg(12, true))
	h.waitMetric(t, "fleet_hub_events_total", 1, `result="known"`)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].Stored || calls[0].HubLastEventSeq != 12 {
		t.Errorf("applier calls %+v, want one for the COALESCE event (no row, seq 12) and none for the known one", calls)
	}
	if rows := h.eventRows(t, m.id); len(rows) != 0 {
		t.Errorf("%d rows after the known event, want 0", len(rows))
	}
}
