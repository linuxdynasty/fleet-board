package outbox_test

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
)

// A refusal the agent sends without logging the command (it answered a
// stale-epoch copy) stores nothing, closes nothing and is not answered:
// the row stays as it was, and the command's copy on the live epoch is
// then acked and answered normally, so its true result is the one kept.
func TestAnUnloggedRefusalLeavesTheCommandForItsReissue(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status typev1.Status
		reason typev1.ReasonCode
		cause  string
		acked  bool
	}{
		{"epoch mismatch on a pending row", typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_EPOCH_MISMATCH, "epoch_mismatch", false},
		{"seq replayed on a delivered row", typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_SEQ_REPLAYED, "seq_replayed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newStore(t)
			streams := newFakeStreams()
			streams.setLive("m1", 1)
			h := newService(t, s, streams, fixedClock(t0))
			ctx := context.Background()
			mustEnqueue(t, s, "m1", "c1")
			h.svc.Ready("m1", 1)
			streams.waitSent(t, 1)
			waitMarked(t, s, "c1", 1, 1)
			e1 := outbox.From{MachineID: "m1", Epoch: 1, Live: true}
			if tc.acked {
				if _, err := h.svc.Ack(ctx, e1, ackOf("c1")); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := readRow(t, s, "c1")
			streams.setLive("m1", 2) // a new stream; the agent answers the stale copy on it
			e2 := outbox.From{MachineID: "m1", Epoch: 2, Live: true}
			late := resultOf("c1", tc.status, "stale copy")
			late.Reason = tc.reason
			if got, err := h.svc.Result(ctx, e2, late); err != nil || got != outbox.Refused {
				t.Fatalf("the unlogged refusal = %v, %v; want Refused", got, err)
			}
			if after, _ := readRow(t, s, "c1"); after.state != before.state || after.version != before.version || after.doneTime.Valid {
				t.Errorf("the refusal changed the row: %+v", after)
			}
			if n := count(t, s, `SELECT count(*) FROM command_results`); n != 0 {
				t.Errorf("%d results stored for the refusal", n)
			}
			if v := sample(h.scrape(t), "fleet_hub_command_messages_total", `arm="command_result"`, `outcome="`+tc.cause+`"`); v != 1 {
				t.Errorf("messages{command_result,%s} = %v, want 1", tc.cause, v)
			}
			if lines := h.events(t, "outbox.refused"); len(lines) != 1 || lines[0]["cause"] != tc.cause {
				t.Errorf("outbox.refused lines %v", lines)
			}
			if !tc.acked { // a pending row goes out again under the live epoch
				h.svc.Gone("m1", 1)
				h.svc.Ready("m1", 2)
				if got := streams.waitSent(t, 2)[1]; got.cmd.GetCommandId().GetValue() != "c1" || got.cmd.GetEpoch() != 2 {
					t.Errorf("re-issue %v", got.cmd)
				}
				if got, err := h.svc.Ack(ctx, e2, ackOf("c1")); err != nil || got != outbox.Applied {
					t.Errorf("the re-issue's ack = %v, %v; want Applied", got, err)
				}
			}
			if got, err := h.svc.Result(ctx, e2, resultOf("c1", typev1.Status_STATUS_OK, "")); err != nil || got != outbox.Applied {
				t.Fatalf("the true result = %v, %v; want Applied", got, err)
			}
			if r, _ := readRow(t, s, "c1"); r.state != "done" || r.status != "OK" || r.reason != "" {
				t.Errorf("row after the true result: %+v", r)
			}
		})
	}
}

// A repeated CommandProgress (the agent re-sends its last one after every
// reconnect) is known and bumps nothing; a new step is applied.
func TestARepeatedProgressChangesNothing(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0))
	mustEnqueue(t, s, "m1", "c1")
	ctx := context.Background()
	p := &agentv1.CommandProgress{CommandId: cid("c1"), Step: "wait_ready", StepIndex: 3, StepCount: 14, Message: "waiting"}
	if got, err := h.svc.Progress(ctx, live1, p); err != nil || got != outbox.Applied {
		t.Fatalf("first progress = %v, %v", got, err)
	}
	first, _ := readRow(t, s, "c1")
	if got, err := h.svc.Progress(ctx, live1, p); err != nil || got != outbox.Known {
		t.Fatalf("repeated progress = %v, %v; want Known", got, err)
	}
	if again, _ := readRow(t, s, "c1"); again.version != first.version || again.hubSeq != first.hubSeq {
		t.Errorf("a repeat bumped the row: %+v -> %+v", first, again)
	}
	for name, next := range map[string]*agentv1.CommandProgress{
		"message": {CommandId: cid("c1"), Step: "wait_ready", StepIndex: 3, StepCount: 14, Message: "still waiting"},
	} {
		if got, _ := h.svc.Progress(ctx, live1, next); got != outbox.Applied {
			t.Errorf("a new %s = %v, want Applied", name, got)
		}
	}
}

// A malformed command_id is refused as invalid and never reaches a log
// line.
func TestAMalformedCommandIDIsRefusedAndNotLogged(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0))
	huge := strings.Repeat("x", 4096)
	for _, id := range []string{huge, "c 1", "c\x1b1"} {
		if got, _ := h.svc.Ack(context.Background(), live1, ackOf(id)); got != outbox.Refused {
			t.Errorf("Ack(%.10q) = %v, want Refused", id, got)
		}
	}
	if v := sample(h.scrape(t), "fleet_hub_command_messages_total", `outcome="invalid"`); v != 3 {
		t.Errorf("messages{invalid} = %v, want 3", v)
	}
	for _, l := range h.events(t, "outbox.refused") {
		if l["command_id"] != "" {
			t.Errorf("a malformed id was logged: %.40v", l["command_id"])
		}
	}
}

// A result's metadata is bounded before it is stored (16 entries of 256
// bytes, no control characters), and a status this hub has no name for is
// stored and counted as UNKNOWN.
func TestResultMetadataAndStatusAreBounded(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	h := newService(t, s, newFakeStreams(), fixedClock(t0))
	mustEnqueue(t, s, "m1", "c1")
	res := resultOf("c1", typev1.Status(9999), "")
	res.Metadata = map[string]string{}
	for i := range 20 {
		res.Metadata[string(rune('a'+i))+"\x07key"] = strings.Repeat("v\x01", 400)
	}
	if got, err := h.svc.Result(context.Background(), live1, res); err != nil || got != outbox.Applied {
		t.Fatalf("Result = %v, %v", got, err)
	}
	md := storedResult(t, s, "c1").GetMetadata()
	if len(md) != 16 {
		t.Errorf("%d metadata entries stored, want 16", len(md))
	}
	for k, v := range md {
		if len(k) > 256 || len(v) > 256 || strings.ContainsAny(k+v, "\x01\x07") || !utf8.ValidString(k+v) {
			t.Errorf("entry %q (%d bytes) is not bounded", k, len(v))
		}
	}
	if r, _ := readRow(t, s, "c1"); r.status != "UNKNOWN" {
		t.Errorf("stored status %q, want UNKNOWN", r.status)
	}
	body := h.scrape(t)
	if v := sample(body, "fleet_hub_commands_total", `status="UNKNOWN"`); v != 1 {
		t.Errorf("commands{UNKNOWN} = %v, want 1", v)
	}
	if strings.Contains(body, `status="9999"`) {
		t.Error("an unnamed status became a label value")
	}
}
