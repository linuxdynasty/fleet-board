package gateway_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// eventRow is one row of the event history.
type eventRow struct {
	seq                    sql.NullInt64
	gen                    int64
	kind, delivery, detail string
	replayed, history      bool
	body                   []byte
}

func (h *hub) eventRows(t *testing.T, id string) []eventRow {
	t.Helper()
	var out []eventRow
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), `SELECT seq, event_generation, kind, delivery, detail, replayed,
			history_only, body FROM events WHERE machine_id = ? ORDER BY id`, id)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r eventRow
			if err := rows.Scan(&r.seq, &r.gen, &r.kind, &r.delivery, &r.detail, &r.replayed, &r.history, &r.body); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// lastEventSeq is the machine's hub_last_event_seq and its stored
// evicted_total.
func (h *hub) lastEventSeq(t *testing.T, id string) (seq, evicted int64) {
	t.Helper()
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT hub_last_event_seq, events_evicted_total FROM machines
			WHERE machine_id = ?`, id).Scan(&seq, &evicted)
	})
	if err != nil {
		t.Fatal(err)
	}
	return seq, evicted
}

// waitSeq polls until the machine's hub_last_event_seq is want.
func (h *hub) waitSeq(t *testing.T, id string, want int64) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for {
		if got, _ := h.lastEventSeq(t, id); got == want {
			return
		}
		if time.Now().After(end) {
			got, _ := h.lastEventSeq(t, id)
			t.Fatalf("hub_last_event_seq %d, want %d", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// eventMsg is an Event of seq with a NEVER_DROP pane_exited body.
func eventMsg(seq uint64, replayed bool) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Seq: seq, Replayed: replayed, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP,
		Body: &agentv1.Event_PaneExited{PaneExited: &agentv1.PaneExited{ExitStatus: 1}},
	}}}
}

// shedMsg is a live SHED resource sample of seq.
func shedMsg(seq uint64) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Seq: seq, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_SHED,
		Body: &agentv1.Event_ResourceSample{ResourceSample: &agentv1.ResourceSample{}},
	}}}
}

// beatWith is a Heartbeat carrying event_seq and evicted_total.
func beatWith(eventSeq, evicted uint64) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Heartbeat{Heartbeat: &agentv1.Heartbeat{
		EventSeq: eventSeq, Events: &agentv1.EventCounters{EvictedTotal: evicted},
	}}}
}

// decode is a stored body as an Event.
func decode(t *testing.T, b []byte) *agentv1.Event {
	t.Helper()
	var ev agentv1.Event
	if err := proto.Unmarshal(b, &ev); err != nil {
		t.Fatal(err)
	}
	return &ev
}

// snapshotted opens a stream for a new machine, says Hello and applies a
// Snapshot of seq: events may come.
func (h *hub) snapshotted(t *testing.T, seq uint64) (machine, *agentStream) {
	t.Helper()
	m := h.enrollMachine(t)
	s := h.open(t, m)
	s.send(t, hello(m, 0))
	s.ack(t)
	s.send(t, snapshotMsg(seq, "laptop", "h1"))
	h.waitRow(t, m.id, "ONLINE", func(r machineRow) bool { return r.state == "ONLINE" })
	return m, s
}

// acks records the EventAcks the hub would send.
type acks struct {
	mu   sync.Mutex
	seqs []uint64
	at   []time.Time
}

func (a *acks) record(_ string, _, seq uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seqs = append(a.seqs, seq)
	a.at = append(a.at, time.Now())
}

func (a *acks) got() ([]uint64, []time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]uint64(nil), a.seqs...), append([]time.Time(nil), a.at...)
}

// eventApplierFunc is an ingest.EventApplier from a function.
type eventApplierFunc func(ctx context.Context, tx *sql.Tx, machineID string, out statedb.EventOut, ev *agentv1.Event) error

func (f eventApplierFunc) ApplyEvent(ctx context.Context, tx *sql.Tx, machineID string, out statedb.EventOut,
	ev *agentv1.Event,
) error {
	return f(ctx, tx, machineID, out, ev)
}
