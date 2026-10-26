package ingest_test

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
)

// History keeps NEVER_DROP, replayed and unknown-class events (03f
// `events`); a live SHED or COALESCE one only moves the seq. kind is the
// body arm, "unknown" for an arm this hub has no name for, whose bytes the
// stored body still carries whole.
func TestEventInKeepsNeverDropReplaysAndUnknownClasses(t *testing.T) {
	t.Parallel()
	exited := &agentv1.Event_PaneExited{PaneExited: &agentv1.PaneExited{}}
	for _, tc := range []struct {
		name      string
		ev        *agentv1.Event
		kind, cls string
		keep      bool
	}{
		{"never drop", &agentv1.Event{Seq: 1, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP, Body: exited}, "pane_exited", "never_drop", true},
		{"live coalesce", &agentv1.Event{Seq: 1, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_COALESCE, Body: exited}, "pane_exited", "coalesce", false},
		{"replayed coalesce", &agentv1.Event{Seq: 1, Replayed: true, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_COALESCE, Body: exited}, "pane_exited", "coalesce", true},
		{"live shed", &agentv1.Event{Seq: 1, Delivery: agentv1.DeliveryClass_DELIVERY_CLASS_SHED, Body: exited}, "pane_exited", "shed", false},
		{"no class", &agentv1.Event{Seq: 1, Body: exited}, "pane_exited", "unspecified", true},
		{"class 9", &agentv1.Event{Seq: 1, Delivery: 9}, "unknown", "unspecified", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in, err := ingest.EventIn(ingest.Stream{MachineID: "m", Epoch: 2}, tc.ev, 7, time.Unix(100, 0))
			if err != nil {
				t.Fatal(err)
			}
			if in.Kind != tc.kind || in.Delivery != tc.cls || in.Keep != tc.keep || in.PrevSeq != 7 || in.Epoch != 2 {
				t.Errorf("in = %+v, want kind %s, class %s, keep %v", in, tc.kind, tc.cls, tc.keep)
			}
		})
	}
	newer := &agentv1.Event{Seq: 3}
	newer.ProtoReflect().SetUnknown(protowire.AppendBytes(protowire.AppendTag(nil, 99, protowire.BytesType), []byte("future")))
	in, err := ingest.EventIn(ingest.Stream{MachineID: "m", Epoch: 1}, newer, 0, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	var back agentv1.Event
	if err := proto.Unmarshal(in.Body, &back); err != nil || in.Kind != "unknown" ||
		!strings.Contains(string(back.ProtoReflect().GetUnknown()), "future") {
		t.Errorf("unknown arm: kind %s, body %x (%v), want the arm's bytes kept", in.Kind, in.Body, err)
	}
}

// The hook event fields (E8, E9, G5) are cut to their caps: harness
// session ids and raw event names are untrusted hook text, and the
// registry carries a machine path and a free harness name.
func TestCleanEventCutsHookText(t *testing.T) {
	t.Parallel()
	hook := &agentv1.Event{Body: &agentv1.Event_HarnessHook{HarnessHook: &agentv1.HarnessHookEvent{
		HarnessSessionId: "sess\x00" + strings.Repeat("s", 1<<20),
		RawEventName:     "stop\x1b" + strings.Repeat("r", 1<<20),
	}}}
	c := ingest.CleanEvent(hook)
	got := hook.GetHarnessHook()
	if len(got.GetHarnessSessionId()) > 256 || strings.Contains(got.GetHarnessSessionId(), "\x00") {
		t.Errorf("session %d bytes, cleaned %+v", len(got.GetHarnessSessionId()), c)
	}
	if len(got.GetRawEventName()) > 128 || strings.Contains(got.GetRawEventName(), "\x1b") {
		t.Errorf("raw name %d bytes, cleaned %+v", len(got.GetRawEventName()), c)
	}
	reg := &agentv1.Event{Body: &agentv1.Event_GoalRegistry{GoalRegistry: &agentv1.GoalRegistryChanged{
		Registration: &agentv1.GoalRegistration{
			Harness:          &typev1.Harness{Name: "codex\x00" + strings.Repeat("c", 1<<20)},
			WorktreePath:     "/wt\x00" + strings.Repeat("w", 1<<20),
			HarnessSessionId: "sess\x00" + strings.Repeat("s", 1<<20),
		},
	}}}
	ingest.CleanEvent(reg)
	entry := reg.GetGoalRegistry().GetRegistration()
	if len(entry.GetHarness().GetName()) > 256 || len(entry.GetWorktreePath()) > 1024 ||
		len(entry.GetHarnessSessionId()) > 256 || strings.Contains(entry.GetWorktreePath(), "\x00") {
		t.Errorf("registry entry not cut: %+v", entry)
	}
}

// The two free-text Event fields are cut to their cap before the
// scrubber; other strings are left to the rows that apply them.
func TestCleanEventCutsThenScrubsTheFreeText(t *testing.T) {
	t.Parallel()
	secret := "ghp_" + strings.Repeat("B", 36)
	usage := &agentv1.Event{Body: &agentv1.Event_UsageReading{UsageReading: &agentv1.UsageReading{
		ParseMessage: "no match " + secret + "\x1b" + strings.Repeat("y", 1<<20),
	}}}
	c := ingest.CleanEvent(usage)
	got := usage.GetUsageReading().GetParseMessage()
	if len(got) > 1024 || strings.Contains(got, secret) || strings.Contains(got, "\x1b") || c.Scrubbed != 1 || c.Cut != 1 ||
		c.ScrubBytes > 1024 {
		t.Errorf("parse_message %d bytes, cleaned %+v", len(got), c)
	}
	other := &agentv1.Event{Body: &agentv1.Event_PaneExited{PaneExited: &agentv1.PaneExited{}}}
	if c := ingest.CleanEvent(other); c != (ingest.Cleaned{}) {
		t.Errorf("an event without free text: %+v", c)
	}
}
