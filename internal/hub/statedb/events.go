package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// eventsSQL is migration 11 (plan row 5, PR 5c2): the event history (03f
// `events`, 1 week; 02i "Replay apply rule").
//
//   - One row per stored event, keyed (machine_id, event_generation, seq)
//     (02i rule 3: history is keyed by generation, so a rolled-back agent's
//     reused numbers never collide with older rows). body is the cleaned
//     Event as protobuf wire bytes, so an arm this hub has no name for is
//     kept whole (events.proto); kind is the oneof field name, "unknown"
//     for such an arm.
//   - history_only is 1 when seq was at or below the Snapshot floor
//     (snapshot_seq) when the row was written: the event ran no rule.
//   - Hub markers (`gap`, `state_reset`) have no seq and no body; detail
//     is their JSON.
//   - event_time is the agent's observed_time (agent clock, NULL when
//     unset); observed_time and hub_seq are the writer job's stamp (hub
//     clock), and the retention prune reads observed_time.
const eventsSQL = `CREATE TABLE events(
	id INTEGER PRIMARY KEY,
	machine_id TEXT NOT NULL CHECK (machine_id <> ''),
	event_generation INTEGER NOT NULL CHECK (event_generation >= 1),
	seq INTEGER CHECK (seq > 0),
	kind TEXT NOT NULL CHECK (kind <> ''),
	delivery TEXT NOT NULL CHECK (delivery IN ('unspecified', 'shed', 'coalesce', 'never_drop', 'hub')),
	replayed INTEGER NOT NULL DEFAULT 0 CHECK (replayed IN (0, 1)),
	history_only INTEGER NOT NULL DEFAULT 0 CHECK (history_only IN (0, 1)),
	event_time INTEGER,
	observed_time INTEGER NOT NULL,
	hub_seq INTEGER NOT NULL CHECK (hub_seq > 0),
	body BLOB,
	detail TEXT NOT NULL DEFAULT '',
	CHECK ((seq IS NULL) = (kind IN ('gap', 'state_reset'))),
	CHECK ((seq IS NULL) = (delivery = 'hub')),
	CHECK ((seq IS NULL) = (body IS NULL))
) STRICT;
CREATE UNIQUE INDEX events_seq ON events(machine_id, event_generation, seq) WHERE seq IS NOT NULL;
CREATE INDEX events_machine_time ON events(machine_id, observed_time);
CREATE INDEX events_time ON events(observed_time);`

// Hub marker kinds on the timeline (02i "Gaps", rule 3).
const (
	MarkerGap        = "gap"
	MarkerStateReset = "state_reset"
)

var (
	// ErrEventNotCurrent: the event's epoch is not the machine's live
	// connection, or that connection has not had its Snapshot applied.
	ErrEventNotCurrent = errors.New("statedb: the event is not on the machine's current, snapshotted connection")
	// ErrEventChainBroken: the event the stream sent before this one is not
	// committed (its job failed), so this one must not move the seq past it.
	ErrEventChainBroken = errors.New("statedb: the stream's previous event is not committed")
	// ErrEventInvalid: no seq, a number above 2^63 - 1, or no hub time.
	ErrEventInvalid = errors.New("statedb: the event is malformed")
)

// EventIn is one Event taken on a live stream, cleaned and encoded before
// the job (never in the transaction).
type EventIn struct {
	MachineID string
	// Epoch is the stream's connection epoch.
	Epoch uint64
	// Seq is Event.seq; PrevSeq the seq of the event the stream submitted
	// before it (0 for the first): the job refuses to run past one that did
	// not commit ([ErrEventChainBroken]).
	Seq, PrevSeq uint64
	// Kind is the body arm's name; Delivery the class the agent stamped
	// (unspecified, shed, coalesce, never_drop).
	Kind, Delivery string
	Replayed       bool
	// Keep writes a history row (every NEVER_DROP event and every replayed
	// one; 03f `events`); otherwise the event only moves the seq and goes
	// through the appliers.
	Keep bool
	// EventTime is the agent's observed_time, zero when unset.
	EventTime time.Time
	// Body is the Event's wire bytes.
	Body []byte
	// Now is the hub clock the caller read when it built the job.
	Now time.Time
}

// EventOutcome is what [ApplyEvent] did.
type EventOutcome int

const (
	// EventApplied: the event is new; it moved hub_last_event_seq.
	EventApplied EventOutcome = iota + 1
	// EventKnown: seq is at or below hub_last_event_seq (a re-send of an
	// event a dead stream carried, or the hub already holds it); nothing
	// was written and no rule may run.
	EventKnown
)

// EventOut is what [ApplyEvent] decided.
type EventOut struct {
	Outcome EventOutcome
	// History: seq was at or below the Snapshot floor, so the event is
	// history only and runs no rule (02i "Replay apply rule").
	History bool
	// Generation is the machine's event_generation the event belongs to.
	Generation uint64
	// HubLastEventSeq is the machine's value after the job.
	HubLastEventSeq uint64
	// Stored says a history row was written.
	Stored bool
	// HubSeq is the stamp the job took on the machine row: the Watch
	// position of the change it made, 0 when nothing was written.
	HubSeq int64
}

// ApplyEvent stores one event in the writer job's tx: the machine must
// exist, not be REVOKED, and hold a live connection of in.Epoch that is
// its last epoch and whose Snapshot was applied ([ErrEventNotCurrent]);
// the previous event of the stream must be committed
// ([ErrEventChainBroken]). A seq at or below hub_last_event_seq is
// [EventKnown] and writes nothing. Otherwise the event gets a history row
// when in.Keep, hub_last_event_seq moves to seq, and the machine is
// stamped (one hub_seq, observed_time = in.Now; row 5 ruling fork 3:
// version is not touched). Errors leave the job to roll back.
func ApplyEvent(ctx context.Context, tx *sql.Tx, in EventIn) (EventOut, error) {
	if err := in.validate(); err != nil {
		return EventOut{}, err
	}
	var (
		state                                     string
		lastEpoch, snapEpoch, snapSeq, hubLast, g int64
		live                                      int64
	)
	err := tx.QueryRowContext(ctx, `SELECT state, last_epoch, snapshot_epoch, snapshot_seq, hub_last_event_seq,
		event_generation,
		EXISTS (SELECT 1 FROM machine_connections c WHERE c.machine_id = m.machine_id AND c.epoch = ? AND c.ended_time IS NULL)
		FROM machines m WHERE machine_id = ?`, int64(in.Epoch), in.MachineID). //nolint:gosec // G115: validated
		Scan(&state, &lastEpoch, &snapEpoch, &snapSeq, &hubLast, &g, &live)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return EventOut{}, ErrMachineUnknown
	case err != nil:
		return EventOut{}, fmt.Errorf("statedb: read machine for event: %w", err)
	case state == "REVOKED":
		return EventOut{}, ErrMachineRevoked
	case live == 0 || uint64(lastEpoch) != in.Epoch || snapEpoch != lastEpoch: //nolint:gosec // G115: CHECK >= 0
		return EventOut{}, fmt.Errorf("%w: epoch %d, machine at %d, snapshot of %d", ErrEventNotCurrent, in.Epoch, lastEpoch,
			snapEpoch)
	case int64(in.PrevSeq) > hubLast: //nolint:gosec // G115: validated
		return EventOut{}, fmt.Errorf("%w: previous seq %d, stored %d", ErrEventChainBroken, in.PrevSeq, hubLast)
	}
	out := EventOut{Generation: uint64(g), HubLastEventSeq: uint64(hubLast)} //nolint:gosec // G115: CHECK >= 0
	seq := int64(in.Seq)                                                     //nolint:gosec // G115: validated
	if seq <= hubLast {
		out.Outcome = EventKnown
		return out, nil
	}
	out.Outcome, out.History, out.HubLastEventSeq = EventApplied, seq <= snapSeq, in.Seq
	st, err := NextStamp(ctx, tx, in.Now)
	if err != nil {
		return EventOut{}, err
	}
	if in.Keep {
		var at any
		if !in.EventTime.IsZero() {
			at = in.EventTime.UnixMilli()
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO events(machine_id, event_generation, seq, kind, delivery, replayed,
			history_only, event_time, observed_time, hub_seq, body) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.MachineID, g, seq, in.Kind, in.Delivery, in.Replayed, out.History, at, st.ObservedTime.UnixMilli(), st.HubSeq,
			in.Body); err != nil {
			return EventOut{}, fmt.Errorf("statedb: store event: %w", err)
		}
		out.Stored = true
	}
	if _, err := tx.ExecContext(ctx, `UPDATE machines SET hub_last_event_seq = ?, observed_time = ?, hub_seq = ?
		WHERE machine_id = ?`, seq, st.ObservedTime.UnixMilli(), st.HubSeq, in.MachineID); err != nil {
		return EventOut{}, fmt.Errorf("statedb: move hub_last_event_seq: %w", err)
	}
	out.HubSeq = st.HubSeq
	return out, nil
}

func (in EventIn) validate() error {
	switch {
	case in.Seq == 0 || in.Seq > math.MaxInt64 || in.PrevSeq > math.MaxInt64 || in.Epoch > math.MaxInt64:
		return fmt.Errorf("%w: seq %d, previous %d, epoch %d", ErrEventInvalid, in.Seq, in.PrevSeq, in.Epoch)
	case in.Now.IsZero() || in.MachineID == "" || in.Kind == "":
		return fmt.Errorf("%w: it needs the machine, its kind and the hub's time", ErrEventInvalid)
	}
	switch in.Delivery {
	case "unspecified", "shed", "coalesce", "never_drop":
		return nil
	}
	return fmt.Errorf("%w: delivery %q", ErrEventInvalid, in.Delivery)
}

// Marker is a hub mark on a machine's timeline: a gap (events were lost)
// or a state reset (an agent rollback). Detail is its JSON.
type Marker struct {
	MachineID, Kind, Detail string
	Now                     time.Time
}

// AppendMarker inserts m in the job's tx under the machine's current
// event_generation, stamped with its own hub_seq.
func AppendMarker(ctx context.Context, tx *sql.Tx, m Marker) error {
	if m.Kind != MarkerGap && m.Kind != MarkerStateReset {
		return fmt.Errorf("%w: marker kind %q", ErrEventInvalid, m.Kind)
	}
	st, err := NextStamp(ctx, tx, m.Now)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO events(machine_id, event_generation, kind, delivery, observed_time, hub_seq,
		detail) SELECT machine_id, event_generation, ?, 'hub', ?, ?, ? FROM machines WHERE machine_id = ?`,
		m.Kind, st.ObservedTime.UnixMilli(), st.HubSeq, m.Detail, m.MachineID)
	if err != nil {
		return fmt.Errorf("statedb: append %s marker: %w", m.Kind, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrMachineUnknown
	}
	return nil
}
