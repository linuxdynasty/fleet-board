package gateway

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

const chainMachine = "01jchainmachine00000000000"

// chainStore is a real state.db whose writer takes one job at a time and
// queues one more, with a machine live on epoch 1 and its Snapshot (seq
// 10) applied.
func chainStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "6f1c2a4e-0d3b-4c5e-9f7a-1b2c3d4e5f60", CACert: []byte("ca"), CAKey: []byte("key"),
		ServerCert: []byte("cert"), ServerKey: []byte("key"), ServerNames: []string{"localhost"},
		AccountSalt: make([]byte, 32), SaltVersion: 1, SettingsEpoch: "0b9d3c1e-6a2f-4e8d-8c7b-5a4f3e2d1c0b",
		CreatedTime: time.Now(),
	}
	if err := statedb.Create(ctx, dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.Options{
		Path: statedb.Path(dir), Name: "state", Migrations: statedb.Migrations(), MustExist: true, BatchMax: 1, QueueSize: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Now().UTC()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO machines(machine_id, name, expected_hostname, drained, install_id, state,
			hostname_mismatch, created_time) VALUES (?, 'm', 'm', 0, 'install-1', 'ENROLLED', 0, ?)`, chainMachine, now.UnixMilli())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := statedb.Hello(ctx, st, statedb.HelloIn{
		MachineID: chainMachine, PeerSerial: "c0001", BootID: "boot-1", InstallID: "install-1", Peer: "192.0.2.1:5000",
		Reason: statedb.ConnectStart, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	p, err := ingest.SubmitSnapshot(ctx, st, ingest.Stream{MachineID: chainMachine, Epoch: 1, InstallID: "install-1"},
		&agentv1.Snapshot{Seq: 10}, nil, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	return st
}

func liveEvent(seq uint64) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Seq: seq, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP,
		Body: &agentv1.Event_PaneExited{PaneExited: &agentv1.PaneExited{}},
	}}}
}

// Review P0 regression: with the writer busy and its queue full, an
// event taken on an ending stream (its context cancelled, as in the
// teardown's drain) cannot be queued. The next event must never be stored
// past it: the stream is broken and ends, and a job for any later event
// names the lost one as its predecessor, so the store's chain check
// refuses it. hub_last_event_seq stays where it was and the agent sends
// the lost event again on its next stream.
func TestAnEventTheWriterCouldNotQueueIsNeverSkipped(t *testing.T) {
	t.Parallel()
	a, logs := newTestDepsLogged(t)
	st := chainStore(t)
	a.store = st
	ctx := context.Background()
	release := make(chan struct{})
	busy, err := st.Submit(ctx, func(*sql.Tx) error { <-release; return nil })
	if err != nil {
		t.Fatal(err)
	}
	filler, err := st.Submit(ctx, func(*sql.Tx) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	s := newStream(ctx, chainMachine, "boot-1", "p", 1, time.Now())
	a.streams.register(s)
	s.snapshotAt(time.Now())
	s.cancel()                           // the stream is ending: the arm's context is done
	eventArm(s.ctx, a, s, liveEvent(11)) // the queue is full: not queued
	close(release)
	for _, done := range []<-chan error{busy, filler} {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	eventArm(s.ctx, a, s, liveEvent(12)) // the queue has room now
	a.settleAll(ctx, s)
	var seq int64
	if err := st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT hub_last_event_seq FROM machines WHERE machine_id = ?`, chainMachine).Scan(&seq)
	}); err != nil {
		t.Fatal(err)
	}
	if seq != 0 {
		t.Errorf("hub_last_event_seq = %d, want 0: event 12 was stored past the lost event 11", seq)
	}
	select {
	case <-s.ended:
	default:
		t.Error("the stream did not end after an event it could not queue")
	}
	// The lost event itself is named, at once: the stream ends on it, not
	// one event later when the store refuses the next.
	if !strings.Contains(logs.String(), `"msg":"event.failed"`) || !strings.Contains(logs.String(), `"seq":11`) ||
		!strings.Contains(logs.String(), `"step":"queue"`) {
		t.Errorf("want event.failed for seq 11 at step queue in %s", logs.String())
	}
}
