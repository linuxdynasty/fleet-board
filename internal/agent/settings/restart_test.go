package settings_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/settings"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// heldAck is the SettingsAck saved in agent.db.
func heldAck(t *testing.T, r *rig) *agentv1.SettingsAck {
	t.Helper()
	var a agentv1.SettingsAck
	if err := proto.Unmarshal(r.held(t).Ack, &a); err != nil {
		t.Fatal(err)
	}
	return &a
}

// A restart under the same policy file keeps the hub's rejections: agent.db
// keeps the message with each invalid field already replaced, so merging it
// again finds nothing to reject, but the hub must go on seeing the rejection
// (03e failure table). The check of a value does not read the policy file,
// so the saved list still holds.
func TestARestartKeepsTheRejections(t *testing.T) {
	t.Parallel()
	labels := hub(7, &typev1.SettingValues{MaxActive: u32(3)})
	labels.Labels = []string{"Not A Label"}
	probe := hub(7, &typev1.SettingValues{MaxActive: u32(3)})
	probe.ProbeIntervalByAccount = map[string]*durationpb.Duration{"acct-1": pd(0)}
	for _, tc := range []struct {
		name, field string
		msg         *agentv1.Settings
	}{
		{"max_windows 0", "max_windows", hub(7, &typev1.SettingValues{MaxActive: u32(3), MaxWindows: u32(0)})},
		{"a bad label", "labels", labels},
		{"a bad account interval", "probe_interval_by_account.acct-1", probe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, &local{hash: "h1"})
			first := r.deliver(t, tc.msg, 1)
			if got := rejected(first); len(got) != 1 || got[tc.field] != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
				t.Fatalf("first ack rejected %v, want %s INVALID_ARGUMENT", got, tc.field)
			}
			r.restart(t, &local{hash: "h1"})
			if a := heldAck(t, r); !proto.Equal(a, first) {
				t.Errorf("saved ack after the restart\n%v\nwant the first one\n%v", a, first)
			}
			if ev := r.events(t, "settings.reevaluated"); len(ev) != 0 {
				t.Errorf("settings.reevaluated under the same file: %v", ev)
			}
			if a := r.deliver(t, tc.msg, 1); !proto.Equal(a, first) {
				t.Errorf("re-delivered generation 7 acked\n%v\nwant the first ack\n%v", a, first)
			}
		})
	}
}

// A restart under a stricter file merges again: the caps are recomputed,
// the rejections stay, and one settings.reevaluated names both hashes. A new
// generation after it is merged fresh, so an old rejection does not leak.
func TestARestartUnderANewFileRecomputesCapsAndKeepsRejections(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{hash: "h1"})
	msg := hub(7, &typev1.SettingValues{MaxActive: u32(8), MaxWindows: u32(0)})
	first := r.deliver(t, msg, 1)
	r.restart(t, &local{hash: "h2", p: &policy.Policy{MaxActive: u32(2)}})
	if ev := r.events(t, "settings.reevaluated"); len(ev) != 1 || ev[0]["old_hash"] != "h1" || ev[0]["new_hash"] != "h2" {
		t.Errorf("settings.reevaluated %v", ev)
	}
	saved := heldAck(t, r)
	if !slices.Equal(saved.GetCappedFields(), []string{"max_active"}) || saved.GetEffective().GetValues().GetMaxActive() != 2 {
		t.Errorf("saved ack %v, want max_active capped to 2", saved)
	}
	if !slices.EqualFunc(saved.GetRejected(), first.GetRejected(), func(a, b *typev1.SettingRejection) bool { return proto.Equal(a, b) }) {
		t.Errorf("rejected %v, want the first ack's %v", saved.GetRejected(), first.GetRejected())
	}
	if a := r.deliver(t, msg, 1); !proto.Equal(a, saved) {
		t.Errorf("re-delivered generation 7 acked %v, want the saved %v", a, saved)
	}
	a := r.deliver(t, hub(8, &typev1.SettingValues{MaxActive: u32(8), MaxWindows: u32(5)}), 2)
	if a.GetGeneration() != 8 || len(a.GetRejected()) != 0 {
		t.Errorf("generation 8 ack %v, want no rejection", a)
	}
}

// In safe mode the re-delivered generation names the field the hub sent
// wrong as POLICY_SAFE_MODE, like every other, and keeps nothing.
func TestARestartInSafeModeRejectsTheCarriedField(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{hash: "h1"})
	msg := hub(7, &typev1.SettingValues{MaxActive: u32(3), MaxWindows: u32(0)})
	r.deliver(t, msg, 1)
	before := r.held(t)
	r.restart(t, &local{safe: true, hash: "broken"})
	a := r.deliver(t, msg, 1)
	safe := typev1.ReasonCode_REASON_CODE_POLICY_SAFE_MODE
	if got := rejected(a); len(got) != 2 || got["max_windows"] != safe || got["max_active"] != safe {
		t.Errorf("rejected %v, want max_active and max_windows POLICY_SAFE_MODE", got)
	}
	if after := r.held(t); !reflect.DeepEqual(after, before) {
		t.Errorf("safe mode changed the held row:\n%+v\n%+v", before, after)
	}
}

// A failed save sends no ack and leaves the effective values as they were;
// the hub's re-send of the same Settings is then merged and acked.
func TestAFailedSaveSendsNoAckAndChangesNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{hash: "h1", p: &policy.Policy{RateLimits: []policy.RateLimit{{CommandKind: "Drain", PerMinute: 4}}}})
	r.deliver(t, hub(3, &typev1.SettingValues{MaxActive: u32(3)}), 1)
	before := r.m.Effective()
	r.store.fails.Store(1)
	msg := hub(4, &typev1.SettingValues{MaxActive: u32(5), RateLimits: []*typev1.RateLimit{{CommandKind: "Drain", PerMinute: 2}}})
	if !r.m.Hook(t.Context(), 1, msg) {
		t.Fatal("hook refused")
	}
	waitFor(t, func() bool { return len(r.events(t, "settings.store_failed")) == 1 })
	if ev := r.events(t, "settings.store_failed"); ev[0]["err_kind"] != "other" {
		t.Errorf("settings.store_failed %v, want err_kind other", ev)
	}
	if n := len(r.send.all()); n != 1 {
		t.Errorf("%d acks, want only the first: a failed save is not acked", n)
	}
	if got := r.m.Effective(); !proto.Equal(got, before) {
		t.Errorf("effective after a failed save %v, want %v", got, before)
	}
	if n, ok := r.m.RateLimit("Drain"); !ok || n != 4 {
		t.Errorf("RateLimit(Drain) = %d, %v after a failed save, want the local 4", n, ok)
	}
	if got := r.metric(t, "fleet_agent_settings_store_failed_total", ""); got != "1" {
		t.Errorf("store_failed counter %q, want 1", got)
	}
	if h := r.held(t); h.Generation != 3 {
		t.Errorf("held generation %d after a failed save, want 3", h.Generation)
	}
	if a := r.deliver(t, msg, 2); a.GetGeneration() != 4 {
		t.Errorf("re-sent Settings acked %v, want generation 4", a)
	}
	if n, ok := r.m.RateLimit("Drain"); !ok || n != 2 || r.held(t).Generation != 4 {
		t.Errorf("RateLimit(Drain) = %d, %v after the re-send, want the hub's 2", n, ok)
	}
}

// An Open that fails after the gauge was registered leaves no callback
// behind: the caller gets no Manager to Close.
func TestAFailedOpenLeavesNoGauge(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{hash: "h1"})
	r.deliver(t, hub(7, &typev1.SettingValues{MaxActive: u32(3)}), 1)
	r.close(t)
	ctx := context.Background()
	db, err := ring.Open(ctx, r.dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	r.logs = &conntest.Buffer{}
	if r.tel, err = telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "boot", LogOutput: r.logs}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.tel.Shutdown(ctx) }()
	st := &failing{DB: db}
	st.fails.Store(1)
	l := &local{hash: "h2", p: &policy.Policy{MaxActive: u32(2)}}
	if _, err := settings.Open(ctx, settings.Options{DB: st, Local: l, Sender: &sender{}, Telemetry: r.tel}); !errors.Is(err, errDiskFull) {
		t.Fatalf("Open = %v, want the failed save", err)
	}
	if got := r.metric(t, "fleet_agent_settings_applied_generation", ""); got != "" {
		t.Errorf("applied generation gauge %q after a failed Open, want none", got)
	}
}
