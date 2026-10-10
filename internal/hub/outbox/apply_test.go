package outbox_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

func cid(id string) *typev1.CommandId { return &typev1.CommandId{Value: id} }

func ackOf(id string) *agentv1.CommandAck { return &agentv1.CommandAck{CommandId: cid(id)} }

func resultOf(id string, st typev1.Status, msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{CommandId: cid(id), Status: st, Message: msg}
}

var live1 = outbox.From{MachineID: "m1", Epoch: 1, Live: true}

// fixedClock is the hub clock of the apply tests.
func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

// A CommandAck moves a pending row to delivered (Operation DELIVERED) with
// a new version and stamp; a second one is a known repeat that changes
// nothing.
func TestAnAckDeliversAPendingCommandOnce(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0.Add(time.Second)))
	op := mustEnqueue(t, s, "m1", "c1")
	got, err := h.svc.Ack(context.Background(), live1, ackOf("c1"))
	if err != nil || got != outbox.Applied {
		t.Fatalf("Ack = %v, %v; want Applied", got, err)
	}
	r, _ := readRow(t, s, "c1")
	if r.state != "delivered" || r.opState != "DELIVERED" || r.version != 2 || r.hubSeq <= op.HubSeq ||
		!r.ackTime.Valid || r.ackTime.Int64 != t0.Add(time.Second).UnixMilli() {
		t.Errorf("after the ack: %+v", r)
	}
	again, err := h.svc.Ack(context.Background(), live1, ackOf("c1"))
	if err != nil || again != outbox.Known {
		t.Fatalf("second Ack = %v, %v; want Known", again, err)
	}
	if r2, _ := readRow(t, s, "c1"); r2.version != 2 || r2.hubSeq != r.hubSeq {
		t.Errorf("a repeated ack changed the row: %+v", r2)
	}
	if len(h.events(t, "outbox.acked")) != 1 {
		t.Errorf("outbox.acked lines %v, want 1", h.events(t, "outbox.acked"))
	}
}

// Progress makes the Operation RUNNING with the step reached; the outbox
// row counts as delivered even when its ack was lost.
func TestProgressRecordsTheStepAndRunning(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0))
	mustEnqueue(t, s, "m1", "c1")
	p := &agentv1.CommandProgress{
		CommandId: cid("c1"), Step: "wait_ready", StepIndex: 5, StepCount: 14,
		Status: typev1.Status_STATUS_OK, Message: "waiting\x07 for the ready line",
	}
	got, err := h.svc.Progress(context.Background(), live1, p)
	if err != nil || got != outbox.Applied {
		t.Fatalf("Progress = %v, %v", got, err)
	}
	r, _ := readRow(t, s, "c1")
	if r.state != "delivered" || r.opState != "RUNNING" || r.step != "wait_ready" || r.stepIndex != 5 ||
		r.stepCount != 14 || r.message != "waiting for the ready line" || r.version != 2 {
		t.Errorf("after progress: %+v", r)
	}
}

// A result closes the row (done, Operation DONE with status and reason),
// stores the result with its scrubbed message once, and counts the
// command by kind and status with its round trip. A repeat is known and
// changes nothing; the first result is kept.
func TestAResultClosesTheCommandOnce(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0.Add(3*time.Second)))
	mustEnqueue(t, s, "m1", "c1")
	if _, err := h.svc.Ack(context.Background(), live1, ackOf("c1")); err != nil {
		t.Fatal(err)
	}
	res := resultOf("c1", typev1.Status_STATUS_FAILED, "push failed for al@example.com")
	res.Reason = typev1.ReasonCode_REASON_CODE_PANE_BUSY
	got, err := h.svc.Result(context.Background(), live1, res)
	if err != nil || got != outbox.Applied {
		t.Fatalf("Result = %v, %v", got, err)
	}
	r, _ := readRow(t, s, "c1")
	if r.state != "done" || r.opState != "DONE" || r.status != "FAILED" || r.reason != "PANE_BUSY" ||
		!r.doneTime.Valid || r.version != 3 || r.message == "" || r.message == res.GetMessage() {
		t.Errorf("after the result: %+v", r)
	}
	stored := storedResult(t, s, "c1")
	if stored.GetMessage() != r.message || stored.GetStatus() != typev1.Status_STATUS_FAILED {
		t.Errorf("stored result %v, row message %q", stored, r.message)
	}
	dup, err := h.svc.Result(context.Background(), live1, resultOf("c1", typev1.Status_STATUS_OK, "other"))
	if err != nil || dup != outbox.Known {
		t.Fatalf("second Result = %v, %v; want Known", dup, err)
	}
	if again := storedResult(t, s, "c1"); again.GetStatus() != typev1.Status_STATUS_FAILED {
		t.Errorf("the second result replaced the first: %v", again)
	}
	body := h.scrape(t)
	if v := sample(body, "fleet_hub_commands_total", `kind="interrupt"`, `status="FAILED"`); v != 1 {
		t.Errorf("fleet_hub_commands_total{interrupt,FAILED} = %v, want 1", v)
	}
	if v := sample(body, "fleet_hub_command_roundtrip_seconds_count", `kind="interrupt"`); v != 1 {
		t.Errorf("roundtrip count = %v, want 1", v)
	}
	if v := sample(body, "fleet_hub_command_roundtrip_seconds_sum", `kind="interrupt"`); v != 3 {
		t.Errorf("roundtrip sum = %v, want 3 (issued at t0, result at t0+3s)", v)
	}
	lines := h.events(t, "outbox.resulted")
	if len(lines) != 1 || lines[0]["status"] != "FAILED" || lines[0]["command_id"] != "c1" {
		t.Errorf("outbox.resulted lines %v", lines)
	}
}

// A result for a command whose ack was lost still closes it.
func TestAResultWithoutAnAckClosesAPendingCommand(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0))
	mustEnqueue(t, s, "m1", "c1")
	if got, err := h.svc.Result(context.Background(), live1, resultOf("c1", typev1.Status_STATUS_OK, "")); err != nil || got != outbox.Applied {
		t.Fatalf("Result = %v, %v", got, err)
	}
	if r, _ := readRow(t, s, "c1"); r.state != "done" || r.opState != "DONE" || r.status != "OK" || r.ackTime.Valid {
		t.Errorf("row %+v", r)
	}
}

// Messages for a command with no row, another machine's command, or from a
// stream that is no longer its machine's live one are refused and
// counted: nothing is written and no row is created.
func TestMessagesForAnotherCommandAreRefusedAndCounted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		from  outbox.From
		id    string
		cause string
	}{
		{"no row", live1, "c9", "unknown_command"},
		{"another machine", outbox.From{MachineID: "m2", Epoch: 1, Live: true}, "c1", "wrong_machine"},
		{"a displaced stream", outbox.From{MachineID: "m1", Epoch: 1, Live: false}, "c1", "stale_stream"},
		{"no id", live1, "", "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			h := newService(t, s, newFakeStreams(), fixedClock(t0))
			mustEnqueue(t, s, "m1", "c1")
			before, _ := readRow(t, s, "c1")
			ctx := context.Background()
			p := &agentv1.CommandProgress{CommandId: cid(tc.id), Step: "x"}
			for arm, apply := range map[string]func() (outbox.Outcome, error){
				"command_ack":      func() (outbox.Outcome, error) { return h.svc.Ack(ctx, tc.from, ackOf(tc.id)) },
				"command_progress": func() (outbox.Outcome, error) { return h.svc.Progress(ctx, tc.from, p) },
				"command_result": func() (outbox.Outcome, error) {
					return h.svc.Result(ctx, tc.from, resultOf(tc.id, typev1.Status_STATUS_OK, "x"))
				},
			} {
				if got, err := apply(); err != nil || got != outbox.Refused {
					t.Errorf("%s = %v, %v; want Refused", arm, got, err)
				}
			}
			if after, _ := readRow(t, s, "c1"); after.version != before.version || after.state != "pending" {
				t.Errorf("a refused message changed c1: %+v", after)
			}
			if n := count(t, s, `SELECT count(*) FROM commands`); n != 1 {
				t.Errorf("%d command rows, want 1", n)
			}
			if n := count(t, s, `SELECT count(*) FROM command_results`); n != 0 {
				t.Errorf("%d result rows, want 0", n)
			}
			body := h.scrape(t)
			for _, arm := range []string{"command_ack", "command_progress", "command_result"} {
				if v := sample(body, "fleet_hub_command_messages_total", `arm="`+arm+`"`, `outcome="`+tc.cause+`"`); v != 1 {
					t.Errorf("fleet_hub_command_messages_total{%s,%s} = %v, want 1", arm, tc.cause, v)
				}
			}
			unknown := sample(body, "fleet_hub_results_unknown_command_total")
			if want := map[bool]float64{true: 1, false: -1}[tc.cause == "unknown_command"]; unknown != want {
				t.Errorf("fleet_hub_results_unknown_command_total = %v, want %v", unknown, want)
			}
			if lines := h.events(t, "outbox.refused"); len(lines) != 3 || lines[0]["cause"] != tc.cause {
				t.Errorf("outbox.refused lines %v", lines)
			}
		})
	}
}

// Messages for a command the hub already closed are known repeats.
func TestAckAndProgressAfterTheResultAreKnown(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0))
	mustEnqueue(t, s, "m1", "c1")
	ctx := context.Background()
	if _, err := h.svc.Result(ctx, live1, resultOf("c1", typev1.Status_STATUS_OK, "")); err != nil {
		t.Fatal(err)
	}
	before, _ := readRow(t, s, "c1")
	if got, _ := h.svc.Ack(ctx, live1, ackOf("c1")); got != outbox.Known {
		t.Errorf("Ack after the result = %v, want Known", got)
	}
	if got, _ := h.svc.Progress(ctx, live1, &agentv1.CommandProgress{CommandId: cid("c1"), Step: "late"}); got != outbox.Known {
		t.Errorf("Progress after the result = %v, want Known", got)
	}
	if after, _ := readRow(t, s, "c1"); after.version != before.version || after.step != "" || after.opState != "DONE" {
		t.Errorf("a late message changed the closed row: %+v", after)
	}
}

func storedResult(t *testing.T, s interface {
	Read(context.Context, func(*sql.Tx) error) error
}, id string,
) *agentv1.CommandResult {
	t.Helper()
	var b []byte
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(), `SELECT result FROM command_results WHERE command_id = ?`, id).Scan(&b)
	})
	if err != nil {
		t.Fatal(err)
	}
	var r agentv1.CommandResult
	if err := proto.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}
