package events_test

import (
	"errors"
	"regexp"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
)

// Fork 3: a test fails when an Event.body arm has no table row (or a row
// names no arm), and every template uses only placeholders the fill knows.
func TestTheTableCoversEveryBodyArmAndKnownPlaceholders(t *testing.T) {
	t.Parallel()
	oneof := (&agentv1.Event{}).ProtoReflect().Descriptor().Oneofs().ByName("body")
	rows := events.Table()
	if len(rows) != oneof.Fields().Len() {
		t.Errorf("the table has %d rows, Event.body %d arms", len(rows), oneof.Fields().Len())
	}
	place := regexp.MustCompile(`<[^>]*>`)
	for _, r := range rows {
		f := oneof.Fields().ByNumber(r.Number)
		if f == nil || string(f.Name()) != r.Name {
			t.Errorf("row %d %q names no Event.body arm", r.Number, r.Name)
		}
		for _, p := range place.FindAllString(r.Key, -1) {
			if !events.KnownPlaceholder(p) {
				t.Errorf("%s: unknown placeholder %s", r.Name, p)
			}
		}
		if (r.Class == agentv1.DeliveryClass_DELIVERY_CLASS_COALESCE) != (r.Key != "") {
			t.Errorf("%s: class %v with key %q", r.Name, r.Class, r.Key)
		}
	}
}

func pane() *typev1.PaneKey {
	return &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: "%3"}
}

// Every COALESCE arm yields its key from a sample body (values worked out
// by hand from the 02i table), and the two that name <profile>, which no
// body carries, are refused rather than stamped with a wrong key.
func TestStampFillsEveryCoalesceKey(t *testing.T) {
	t.Parallel()
	ak := &typev1.AccountKey{Value: "ab12"}
	cases := []struct {
		name string
		ev   *agentv1.Event
		key  string
	}{
		{"probe_health", &agentv1.Event{Body: &agentv1.Event_ProbeHealth{ProbeHealth: &agentv1.ProbeHealth{ProbeId: &typev1.ProbeId{Value: "p7"}}}}, "probe:p7"},
		{"usage_reading", &agentv1.Event{Body: &agentv1.Event_UsageReading{UsageReading: &agentv1.UsageReading{AccountKey: ak}}}, "usage:ab12"},
		{"session_account_link", &agentv1.Event{Body: &agentv1.Event_SessionAccountLink{SessionAccountLink: &agentv1.SessionAccountLink{Pane: pane()}}}, "link:default/s1/%3"},
		{"harness_ready", &agentv1.Event{Body: &agentv1.Event_HarnessReady{HarnessReady: &agentv1.HarnessReady{Pane: pane()}}}, "ready:default/s1/%3"},
		{"pane_input", &agentv1.Event{Body: &agentv1.Event_PaneInput{PaneInput: &agentv1.PaneInput{Pane: pane()}}}, "input:default/s1/%3"},
		{"pane_changed", &agentv1.Event{Body: &agentv1.Event_PaneChanged{PaneChanged: &agentv1.PaneChanged{Pane: &agentv1.TmuxPane{Key: pane()}}}}, "pane:default/s1/%3"},
		{"branch_state", &agentv1.Event{Body: &agentv1.Event_BranchState{BranchState: &agentv1.BranchState{Path: "/w/repo-1"}}}, "branch:/w/repo-1"},
		{"forge_cli_status", &agentv1.Event{Body: &agentv1.Event_ForgeCliStatus{ForgeCliStatus: &agentv1.ForgeCliStatus{Tool: "gh", Host: "github.com"}}}, "forge:gh:github.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := events.Stamp(tc.ev); err != nil {
				t.Fatal(err)
			}
			if tc.ev.GetDelivery() != agentv1.DeliveryClass_DELIVERY_CLASS_COALESCE || tc.ev.GetCoalesceKey() != tc.key {
				t.Errorf("stamped %v %q, want COALESCE %q", tc.ev.GetDelivery(), tc.ev.GetCoalesceKey(), tc.key)
			}
		})
	}
	h := &typev1.Harness{Name: "claude"}
	for _, e := range []*agentv1.Event{
		{Body: &agentv1.Event_AccountIdentity{AccountIdentity: &agentv1.AccountIdentity{Harness: h}}},
		{Body: &agentv1.Event_HarnessChanged{HarnessChanged: &agentv1.HarnessInfo{Harness: h}}},
	} {
		if err := events.Stamp(e); !errors.Is(err, events.ErrNoKey) {
			t.Errorf("%T: %v, want ErrNoKey (no body carries <profile>)", e.GetBody(), err)
		}
	}
	// A COALESCE body without the field its key names is refused too.
	if err := events.Stamp(&agentv1.Event{Body: &agentv1.Event_PaneInput{PaneInput: &agentv1.PaneInput{}}}); !errors.Is(err, events.ErrNoKey) {
		t.Errorf("a pane_input with no pane: %v, want ErrNoKey", err)
	}
}

func TestStampSetsTheClassAndNoKeyForOtherClasses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		ev   *agentv1.Event
		want agentv1.DeliveryClass
	}{
		{&agentv1.Event{Body: &agentv1.Event_ResourceSample{ResourceSample: &agentv1.ResourceSample{}}}, agentv1.DeliveryClass_DELIVERY_CLASS_SHED},
		{&agentv1.Event{Body: &agentv1.Event_PolicyChanged{PolicyChanged: &agentv1.PolicyChanged{}}, CoalesceKey: "stale"}, agentv1.DeliveryClass_DELIVERY_CLASS_NEVER_DROP},
	} {
		if err := events.Stamp(tc.ev); err != nil || tc.ev.GetDelivery() != tc.want || tc.ev.GetCoalesceKey() != "" {
			t.Errorf("%T: %v %q (%v), want %v and no key", tc.ev.GetBody(), tc.ev.GetDelivery(), tc.ev.GetCoalesceKey(), err, tc.want)
		}
	}
	if err := events.Stamp(&agentv1.Event{}); !errors.Is(err, events.ErrNoBody) {
		t.Errorf("an event with no body: %v", err)
	}
}
