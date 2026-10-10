package gateway_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// enqueue puts an Interrupt for machine in the outbox through the store's
// writer (outbox.Enqueue in the caller's job, the product API) and tells
// the hub after the commit.
func (h *hub) enqueue(t *testing.T, machine, id string) outbox.Op {
	t.Helper()
	cmd := &agentv1.Command{
		CommandId:    &typev1.CommandId{Value: id},
		NotAfterTime: timestamppb.New(h.clock.now().Add(time.Hour)),
		Kind:         &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{Pane: &typev1.PaneKey{Server: "default"}}},
	}
	var op outbox.Op
	err := h.store.Write(context.Background(), func(tx *sql.Tx) error {
		var err error
		op, err = outbox.Enqueue(context.Background(), tx, outbox.In{MachineID: machine, Command: cmd, Now: h.clock.now()})
		return err
	})
	if err != nil {
		t.Fatalf("Enqueue %s: %v", id, err)
	}
	h.srv.Outbox().Notify(op)
	return op
}

// command is the next message from the hub, which must be a Command.
func (s *agentStream) command(t *testing.T) *agentv1.Command {
	t.Helper()
	m := s.next(t)
	if m.GetCommand() == nil {
		t.Fatalf("message %v, want a Command", m)
	}
	return m.GetCommand()
}

func reqAck(id string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandAck{CommandAck: &agentv1.CommandAck{
		CommandId: &typev1.CommandId{Value: id}, ReceivedTime: timestamppb.Now(),
	}}}
}

func reqResult(id, msg string) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
		CommandId: &typev1.CommandId{Value: id}, Status: typev1.Status_STATUS_OK, Message: msg,
	}}}
}

// opRow is a commands row's state, Operation state, result message and
// last send.
type opRow struct {
	state, opState, message string
	sentEpoch, sentSeq      sql.NullInt64
}

func (h *hub) opRow(t *testing.T, id string) opRow {
	t.Helper()
	var r opRow
	err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT state, op_state, message, sent_epoch, sent_seq
			FROM commands WHERE command_id = ?`, id).Scan(&r.state, &r.opState, &r.message, &r.sentEpoch, &r.sentSeq)
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// connected opens a stream for m and returns it with its HelloAck epoch.
func (h *hub) connected(t *testing.T, m machine, lastEpoch uint64) (*agentStream, uint64) {
	t.Helper()
	s := h.open(t, m)
	s.send(t, hello(m, lastEpoch))
	return s, s.ack(t).GetEpoch()
}

// The round trip through the real listener: a command enqueued for a
// connected machine waits for its Snapshot (the Ready seam), then goes out
// with the stream's epoch and seq 1; the agent's CommandAck, CommandProgress
// and CommandResult are applied, and ResultAck follows the result's commit.
// The stored message is scrubbed.
func TestACommandMakesTheRoundTrip(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s, epoch := h.connected(t, m, 0)
	h.enqueue(t, m.id, "c1")
	s.silent(t, 300*time.Millisecond) // no command between Hello and Snapshot
	if !gateway.Ready(h.srv, m.id, epoch) {
		t.Fatal("Ready refused the live epoch")
	}
	cmd := s.command(t)
	if cmd.GetCommandId().GetValue() != "c1" || cmd.GetEpoch() != epoch || cmd.GetSeq() != 1 || cmd.GetInterrupt() == nil {
		t.Fatalf("Command = %v", cmd)
	}
	s.send(t, reqAck("c1"))
	s.send(t, &agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_CommandProgress{CommandProgress: &agentv1.CommandProgress{
		CommandId: &typev1.CommandId{Value: "c1"}, Step: "interrupt",
	}}})
	s.send(t, reqResult("c1", "sent to al@example.com"))
	ra := s.next(t).GetResultAck()
	if ra == nil || len(ra.GetCommandIds()) != 1 || ra.GetCommandIds()[0].GetValue() != "c1" {
		t.Fatalf("want ResultAck{c1}, got %v", ra)
	}
	r := h.opRow(t, "c1") // ResultAck follows the commit, so the row is already closed
	if r.state != "done" || r.opState != "DONE" || strings.Contains(r.message, "al@example.com") || r.message == "" {
		t.Errorf("row after the round trip: %+v", r)
	}
	if r.sentEpoch.Int64 != int64(epoch) || r.sentSeq.Int64 != 1 { //nolint:gosec // G115: a test epoch
		t.Errorf("recorded send %v/%v, want %d/1", r.sentEpoch, r.sentSeq, epoch)
	}
	body := scrape(t, h.fixture)
	if v := sample(body, "fleet_hub_commands_total", `kind="interrupt"`, `status="OK"`); v != 1 {
		t.Errorf("fleet_hub_commands_total = %v, want 1", v)
	}
	for _, arm := range []string{"command_ack", "command_progress", "command_result"} {
		if v := sample(body, "fleet_hub_command_messages_total", `arm="`+arm+`"`, `outcome="applied"`); v != 1 {
			t.Errorf("%s applied = %v, want 1", arm, v)
		}
		if v := sample(body, "fleet_hub_connect_unhandled_total", `kind="`+arm+`"`); v != -1 {
			t.Errorf("%s still counted unhandled: %v", arm, v)
		}
	}
	// A repeated result is answered again and changes nothing.
	s.send(t, reqResult("c1", "other"))
	if ra := s.next(t).GetResultAck(); ra == nil || ra.GetCommandIds()[0].GetValue() != "c1" {
		t.Fatalf("want a second ResultAck{c1}, got %v", ra)
	}
}

// A result for a command the hub has no row for is refused: no row, no
// ResultAck.
func TestAResultForAnUnknownCommandIsNotAcknowledged(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s, _ := h.connected(t, m, 0)
	s.send(t, reqResult("nope", "x"))
	h.waitMetric(t, "fleet_hub_results_unknown_command_total", 1)
	s.silent(t, 200*time.Millisecond)
	var n int
	if err := h.store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT count(*) FROM commands`).Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("commands rows %d (%v), want 0", n, err)
	}
}

// Ready is refused for an epoch that is not the machine's live stream.
func TestReadyNeedsTheLiveEpoch(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s, epoch := h.connected(t, m, 0)
	h.enqueue(t, m.id, "c1")
	if gateway.Ready(h.srv, m.id, epoch+1) || gateway.Ready(h.srv, "nobody", 1) {
		t.Error("Ready accepted an epoch with no live stream")
	}
	s.silent(t, 300*time.Millisecond)
}

// A stream that dies with commands unacknowledged leaves them pending; a
// command enqueued while no stream is live waits; the next stream's Ready
// re-sends all of them, in enqueue order, with the same command_ids and
// the new epoch's seqs from 1.
func TestAResendAfterAStreamDiesUsesTheNewEpoch(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	first, e1 := h.connected(t, m, 0)
	h.enqueue(t, m.id, "c1")
	gateway.Ready(h.srv, m.id, e1)
	if got := first.command(t); got.GetSeq() != 1 || got.GetEpoch() != e1 {
		t.Fatalf("first send %v", got)
	}
	first.send(t, agentBye()) // the stream ends before the agent acks
	h.waitConns(t, m.id, "the first row closed", func(c []conn) bool { return len(c) == 1 && c[0].endReason != "" })
	h.enqueue(t, m.id, "c2")
	if r := h.opRow(t, "c2"); r.state != "pending" || r.sentEpoch.Valid {
		t.Fatalf("c2 with no live stream: %+v", r)
	}
	h.clock.set(h.clock.now().Add(6 * time.Second)) // past the Hello rate limit
	second, e2 := h.connected(t, m, e1)
	gateway.Ready(h.srv, m.id, e2)
	for i, want := range []string{"c1", "c2"} {
		got := second.command(t)
		if got.GetCommandId().GetValue() != want || got.GetEpoch() != e2 || got.GetSeq() != uint64(i+1) {
			t.Errorf("re-send %d = %s epoch %d seq %d, want %s %d %d", i, got.GetCommandId().GetValue(),
				got.GetEpoch(), got.GetSeq(), want, e2, i+1)
		}
	}
}

// A command enqueued before a hub restart, never sent, goes out on the
// first epoch after it, with seq 1.
func TestACommandEnqueuedBeforeARestartIsSentAfterIt(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	old, e1 := h.connected(t, m, 0)
	for _, id := range []string{"c1", "c2", "c3"} {
		h.enqueue(t, m.id, id)
	}
	h.shutdown(t)
	if got := old.bye(t); got != agentv1.ByeReason_BYE_REASON_HUB_RESTART {
		t.Fatalf("Bye %v", got)
	}
	h2 := startHubOn(t, h.fixture)
	s, e2 := h2.connected(t, m, e1)
	gateway.Ready(h2.srv, m.id, e2)
	for i, want := range []string{"c1", "c2", "c3"} {
		got := s.command(t)
		if got.GetCommandId().GetValue() != want || got.GetEpoch() != e2 || got.GetSeq() != uint64(i+1) {
			t.Errorf("send %d after the restart: %v", i, got)
		}
	}
}
