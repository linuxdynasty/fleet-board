package settings_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// A fresh agent holds no settings: the defaults are in effect at
// generation 0 and the snapshot says so.
func TestAFreshAgentReportsTheDefaults(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{})
	var s agentv1.Snapshot
	if err := r.m.Snapshot(t.Context(), &s); err != nil {
		t.Fatal(err)
	}
	if s.GetSettings().GetGeneration() != 0 || s.GetSettings().GetValues().GetMaxActive() != 2 {
		t.Errorf("snapshot settings %v", s.GetSettings())
	}
	if _, ok := r.m.RateLimit("Drain"); ok {
		t.Error("a rate limit with no layer stating one")
	}
}

// A Settings is merged, saved in agent.db before its ack is queued, and
// then read by the executor and the snapshot.
func TestSettingsAreSavedThenAckedAndApplied(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{hash: "h1", p: &policy.Policy{RateLimits: []policy.RateLimit{{CommandKind: "Drain", PerMinute: 4}}}})
	h := hub(13, &typev1.SettingValues{
		MaxActive: u32(3), RateLimits: []*typev1.RateLimit{{CommandKind: "Drain", PerMinute: 10}},
		LocalApproval: []*typev1.LocalApprovalRule{{CommandKind: "KillWindow", Required: true}},
	})
	a := r.deliver(t, h, 1)
	if a.GetGeneration() != 13 || a.GetSettingsEpoch() != "e1" || !slices.Equal(a.GetCappedFields(), []string{"rate_limits"}) {
		t.Errorf("ack %v", a)
	}
	held := r.held(t)
	if held.Epoch != "e1" || held.Generation != 13 || held.PolicyHash != "h1" {
		t.Errorf("held %+v", held)
	}
	var ack agentv1.SettingsAck
	if err := proto.Unmarshal(held.Ack, &ack); err != nil || !proto.Equal(&ack, a) {
		t.Errorf("held ack %v (%v), want the one sent", &ack, err)
	}
	if n, ok := r.m.RateLimit("Drain"); !ok || n != 4 {
		t.Errorf("RateLimit(Drain) = %d, %v; want the local 4", n, ok)
	}
	if !r.m.ApprovalRequired("KillWindow") || r.m.ApprovalRequired("Drain") || r.m.SafeMode() {
		t.Error("approval or safe mode wrong after the merge")
	}
	var s agentv1.Snapshot
	_ = r.m.Snapshot(t.Context(), &s)
	if s.GetSettings().GetGeneration() != 13 || s.GetSettings().GetValues().GetMaxActive() != 3 {
		t.Errorf("snapshot settings %v", s.GetSettings())
	}
	if r.local.recheck.Load() < 1 {
		t.Error("the policy file was not re-hashed when Settings arrived (ruling fork 7)")
	}
	if ev := r.events(t, "settings.applied"); len(ev) != 1 || ev[0]["generation"] != float64(13) || ev[0]["capped"] != float64(1) {
		t.Errorf("settings.applied %v", ev)
	}
	if ev := r.events(t, "settings.capped"); len(ev) != 1 || ev[0]["field"] != "rate_limits" {
		t.Errorf("settings.capped %v", ev)
	}
	if got := r.metric(t, "fleet_agent_settings_applied_generation", ""); got != "13" {
		t.Errorf("applied generation gauge %q", got)
	}
	if got := r.metric(t, "fleet_agent_settings_capped_total", `field="rate_limits"`); got != "1" {
		t.Errorf("capped counter %q", got)
	}
}

// A repeat or a lower generation in the same epoch is not applied again: the
// held ack is sent again. A new epoch is taken whatever its generation
// (02-contracts "Settings order").
func TestTheOrderOfSettingsIsKept(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{})
	first := r.deliver(t, hub(13, &typev1.SettingValues{MaxActive: u32(3)}), 1)
	saved := r.held(t).Applied
	for i, s := range []*agentv1.Settings{
		hub(13, &typev1.SettingValues{MaxActive: u32(5)}),
		hub(12, &typev1.SettingValues{MaxActive: u32(6)}),
	} {
		a := r.deliver(t, s, i+2)
		if !proto.Equal(a, first) {
			t.Errorf("generation %d: ack %v, want the held one again", s.GetGeneration(), a)
		}
	}
	if h := r.held(t); h.Generation != 13 || !h.Applied.Equal(saved) {
		t.Errorf("held %+v after a repeat and a lower generation", h)
	}
	if v := r.m.Effective().GetValues().GetMaxActive(); v != 3 {
		t.Errorf("max_active %d, want 3 still", v)
	}
	if got := r.metric(t, "fleet_agent_settings_ignored_total", `reason="older"`); got != "1" {
		t.Errorf("ignored older %q", got)
	}
	restored := hub(2, &typev1.SettingValues{MaxActive: u32(1)})
	restored.SettingsEpoch = "e2"
	if a := r.deliver(t, restored, 4); a.GetGeneration() != 2 || a.GetSettingsEpoch() != "e2" {
		t.Errorf("new epoch ack %v", a)
	}
	if h := r.held(t); h.Epoch != "e2" || h.Generation != 2 || r.m.Effective().GetValues().GetMaxActive() != 1 {
		t.Errorf("held %+v after a new epoch", h)
	}
	if !r.m.Hook(t.Context(), 1, &agentv1.Settings{Generation: 9}) {
		t.Fatal("hook refused")
	}
	waitFor(t, func() bool { return len(r.events(t, "settings.ignored")) == 3 })
	if len(r.send.all()) != 4 {
		t.Errorf("a Settings with no epoch was acked")
	}
}

// The file is the ceiling: a restart under a stricter policy merges the held
// hub settings again, and the snapshot shows the new cap (03e "Generation";
// the loaded policy changes only at a restart).
func TestARestartMergesTheHeldSettingsUnderTheNewPolicy(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{hash: "h1"})
	r.deliver(t, hub(7, &typev1.SettingValues{MaxActive: u32(8)}), 1)
	r.restart(t, &local{hash: "h2", p: &policy.Policy{MaxActive: u32(2)}})
	e := r.m.Effective()
	if e.GetGeneration() != 7 || e.GetValues().GetMaxActive() != 2 {
		t.Errorf("effective after the restart %v", e)
	}
	h := r.held(t)
	var ack agentv1.SettingsAck
	if err := proto.Unmarshal(h.Ack, &ack); err != nil || h.PolicyHash != "h2" || h.Generation != 7 ||
		!slices.Equal(ack.GetCappedFields(), []string{"max_active"}) {
		t.Errorf("held %+v ack %v", h, &ack)
	}
	if ev := r.events(t, "settings.reevaluated"); len(ev) != 1 || ev[0]["old_hash"] != "h1" || ev[0]["new_hash"] != "h2" {
		t.Errorf("settings.reevaluated %v", ev)
	}
	// A repeat of generation 7 now re-acks the merge under the new file.
	if a := r.deliver(t, hub(7, &typev1.SettingValues{MaxActive: u32(8)}), 1); !proto.Equal(a, &ack) {
		t.Errorf("re-ack %v, want %v", a, &ack)
	}
}

// Safe mode rejects every hub setting but keeps them, so once the owner fixes
// the file and restarts, they apply without the hub sending them again.
func TestSafeModeKeepsTheHubSettingsForTheFixedFile(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{safe: true})
	a := r.deliver(t, hub(8, &typev1.SettingValues{MaxActive: u32(1)}), 1)
	if len(a.GetRejected()) != 1 || a.GetRejected()[0].GetReason() != typev1.ReasonCode_REASON_CODE_POLICY_SAFE_MODE {
		t.Errorf("ack in safe mode %v", a)
	}
	if !r.m.SafeMode() || r.m.Effective().GetValues().GetMaxActive() != 2 {
		t.Errorf("safe mode %v, max_active %d", r.m.SafeMode(), r.m.Effective().GetValues().GetMaxActive())
	}
	r.restart(t, &local{})
	if v := r.m.Effective().GetValues().GetMaxActive(); v != 1 {
		t.Errorf("max_active after the fix %d, want the hub's 1", v)
	}
}

// A message the hook takes waits in a bounded intake; a full intake refuses
// (command.intake_full). A failed send is logged and the hub re-sends on
// connect.
func TestTheHookNeverBlocksAndAFailedSendIsLogged(t *testing.T) {
	t.Parallel()
	r := newRig(t, &local{})
	r.send.mu.Lock()
	r.send.fail = conn.ErrNoStream
	r.send.mu.Unlock()
	if !r.m.Hook(t.Context(), 1, hub(3, nil)) {
		t.Fatal("hook refused")
	}
	waitFor(t, func() bool { return len(r.events(t, "settings.ack_unsent")) == 1 })
	if h := r.held(t); h.Generation != 3 {
		t.Errorf("held %+v: the settings are saved before the send", h)
	}
	r.close(t) // no worker: the intake fills up
	took := 0
	for i := range 64 {
		if r.m.Hook(context.Background(), 1, hub(uint64(10+i), nil)) {
			took++
		}
	}
	if took == 0 || took == 64 {
		t.Errorf("the hook took %d of 64 with no worker, want a bounded intake", took)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("not true within %v", wait)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
