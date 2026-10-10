package outbox_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

// newStore is a real state.db in t.TempDir() with machines m1 and m2.
func newStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "state")
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "fleet-1", CACert: []byte("ca"), CAKey: []byte("key"), ServerCert: []byte("cert"),
		ServerKey: []byte("skey"), ServerNames: []string{"hub"}, AccountSalt: bytes.Repeat([]byte{7}, 32),
		SaltVersion: 1, SettingsEpoch: "epoch-1", CreatedTime: t0,
	}
	if err := statedb.Create(ctx, dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s, err := statedb.Open(ctx, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	err = s.Write(ctx, func(tx *sql.Tx) error {
		for _, m := range []string{"m1", "m2"} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO machines(machine_id, name, expected_hostname, drained,
				install_id, state, hostname_mismatch, created_time) VALUES (?, ?, ?, 0, 'i', 'ENROLLED', 0, 1)`,
				m, m, m); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// interrupt is an Interrupt command with id (empty: Enqueue mints one),
// due an hour after t0.
func interrupt(id string) *agentv1.Command {
	c := &agentv1.Command{
		NotAfterTime: timestamppb.New(t0.Add(time.Hour)),
		IssuedTime:   timestamppb.New(t0),
		RequestedBy:  &typev1.Principal{Name: "alice", Id: &typev1.PrincipalId{Value: "u-alice"}},
		Kind:         &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: &typev1.PaneKey{Server: "default"}}},
	}
	if id != "" {
		c.CommandId = &typev1.CommandId{Value: id}
	}
	return c
}

// enqueue runs outbox.Enqueue in a writer job of its own, the way a caller
// would inside its decision's job.
func enqueue(t *testing.T, s *store.Store, in outbox.In) (outbox.Op, error) {
	t.Helper()
	var op outbox.Op
	err := s.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		op, err = outbox.Enqueue(context.Background(), tx, in)
		return err
	})
	return op, err
}

func mustEnqueue(t *testing.T, s *store.Store, machine, id string) outbox.Op {
	t.Helper()
	op, err := enqueue(t, s, outbox.In{MachineID: machine, Command: interrupt(id), RequestID: "req-" + id, Now: t0})
	if err != nil {
		t.Fatalf("Enqueue %s: %v", id, err)
	}
	return op
}

// row is the stored commands row of one command_id.
type row struct {
	machine, kind, state, opState, status, reason, step, message string
	stepIndex, stepCount, version, hubSeq                        int64
	sentEpoch, sentSeq, ackTime, doneTime                        sql.NullInt64
	command                                                      []byte
}

func readRow(t *testing.T, s *store.Store, id string) (row, bool) {
	t.Helper()
	var r row
	found := true
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		err := tx.QueryRowContext(context.Background(), `SELECT machine_id, kind, state, op_state, status, reason,
			step, message, step_index, step_count, version, hub_seq, sent_epoch, sent_seq, ack_time, done_time, command
			FROM commands WHERE command_id = ?`, id).Scan(&r.machine, &r.kind, &r.state, &r.opState, &r.status,
			&r.reason, &r.step, &r.message, &r.stepIndex, &r.stepCount, &r.version, &r.hubSeq, &r.sentEpoch,
			&r.sentSeq, &r.ackTime, &r.doneTime, &r.command)
		if err == sql.ErrNoRows {
			found = false
			return nil
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return r, found
}

func count(t *testing.T, s *store.Store, query string, args ...any) int {
	t.Helper()
	var n int
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fakeStreams is outbox.Streams with one live epoch per machine; it
// records what it queued, or refuses with full or not_live.
type fakeStreams struct {
	mu     sync.Mutex
	live   map[string]uint64
	full   bool
	sent   []sent
	notify chan struct{}
}

type sent struct {
	machine string
	epoch   uint64
	cmd     *agentv1.Command
}

func newFakeStreams() *fakeStreams {
	return &fakeStreams{live: map[string]uint64{}, notify: make(chan struct{}, 100)}
}

func (f *fakeStreams) Send(machineID string, epoch uint64, m *agentv1.ConnectResponse) outbox.SendResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case f.live[machineID] != epoch:
		return outbox.SendNotLive
	case f.full:
		return outbox.SendFull
	}
	f.sent = append(f.sent, sent{machine: machineID, epoch: epoch, cmd: proto.CloneOf(m.GetCommand())})
	select {
	case f.notify <- struct{}{}:
	default:
	}
	return outbox.SendQueued
}

func (f *fakeStreams) setLive(machine string, epoch uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live[machine] = epoch
}

func (f *fakeStreams) setFull(full bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.full = full
}

func (f *fakeStreams) got() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.sent...)
}

// waitSent waits until n commands were queued in all.
func (f *fakeStreams) waitSent(t *testing.T, n int) []sent {
	t.Helper()
	end := time.After(10 * time.Second)
	for {
		if got := f.got(); len(got) >= n {
			return got
		}
		select {
		case <-f.notify:
		case <-end:
			t.Fatalf("queued %d commands, want %d", len(f.got()), n)
		}
	}
}

// harness is a running Service with its telemetry.
type harness struct {
	svc  *outbox.Service
	tel  *telemetry.Telemetry
	logs *syncBuffer
}

// newService runs a Service on s and streams until the test ends.
func newService(t *testing.T, s *store.Store, streams outbox.Streams, now func() time.Time) *harness {
	t.Helper()
	logs := &syncBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "t", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	svc, err := outbox.New(outbox.Options{Store: s, Streams: streams, Telemetry: tel, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); svc.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done; svc.Close() })
	return &harness{svc: svc, tel: tel, logs: logs}
}

// scrape is the Prometheus text of h's metrics.
func (h *harness) scrape(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	h.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

// sample is the value of the first sample of name whose line holds every
// label in want, or -1.
func sample(body, name string, want ...string) float64 {
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, name+"{") && !strings.HasPrefix(line, name+" ") {
			continue
		}
		match := true
		for _, w := range want {
			match = match && strings.Contains(line, w)
		}
		if match {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				return -1
			}
			return v
		}
	}
	return -1
}

// events are the JSON log lines whose msg is name.
func (h *harness) events(t *testing.T, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(h.logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
