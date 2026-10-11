package settings_test

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/settings"
)

// rejected maps each rejected field to its reason; a rejection with no
// message, or one over 256 bytes, is listed under "<field> message".
func rejected(a *agentv1.SettingsAck) map[string]typev1.ReasonCode {
	out := map[string]typev1.ReasonCode{}
	for _, r := range a.GetRejected() {
		out[r.GetField()] = r.GetReason()
		if n := len(r.GetMessage()); n == 0 || n > 256 {
			out[r.GetField()+" message"] = typev1.ReasonCode_REASON_CODE_UNSPECIFIED
		}
	}
	return out
}

// A value the agent cannot apply is rejected (INVALID_ARGUMENT, never a
// cap) and the field keeps the value held before (03e failure table).
func TestAnInvalidHubValueIsRejectedAndThePreviousOneStands(t *testing.T) {
	t.Parallel()
	prev := hub(5, &typev1.SettingValues{MaxActive: u32(3), MaxWindows: u32(7)})
	bad := []struct {
		field string
		v     *typev1.SettingValues
	}{
		{"max_active", &typev1.SettingValues{MaxActive: u32(17)}},
		{"max_active", &typev1.SettingValues{MaxActive: u32(0)}},
		{"max_total", &typev1.SettingValues{MaxTotal: u32(33)}},
		{"max_windows", &typev1.SettingValues{MaxWindows: u32(0)}},
		{"cert_lifetime", &typev1.SettingValues{CertLifetime: pd(366 * 24 * time.Hour)}},
		{"cert_lifetime", &typev1.SettingValues{CertLifetime: pd(0)}},
		{"attach_idle_timeout", &typev1.SettingValues{AttachIdleTimeout: pd(-time.Second)}},
		{"idle_threshold", &typev1.SettingValues{IdleThreshold: &durationpb.Duration{Seconds: 1, Nanos: -1}}},
		{"probe_interval", &typev1.SettingValues{ProbeInterval: pd(0)}},
		{"default_server", &typev1.SettingValues{DefaultServer: typev1.DefaultServerMode_DEFAULT_SERVER_MODE_UNSPECIFIED.Enum()}},
		{"default_server", &typev1.SettingValues{DefaultServer: typev1.DefaultServerMode(9).Enum()}},
		{"allowed_harnesses", &typev1.SettingValues{AllowedHarnesses: []string{"Claude Code"}}},
		{"allowed_repo_roots", &typev1.SettingValues{AllowedRepoRoots: []string{"repos"}}},
		{"allowed_repo_roots", &typev1.SettingValues{AllowedRepoRoots: []string{"/r/\x1b[2J"}}},
		{"local_approval", &typev1.SettingValues{LocalApproval: []*typev1.LocalApprovalRule{{CommandKind: "Format", Required: true}}}},
		{"local_approval", &typev1.SettingValues{LocalApproval: []*typev1.LocalApprovalRule{{CommandKind: "Drain"}, {CommandKind: "Drain", Required: true}}}},
		{"rate_limits", &typev1.SettingValues{RateLimits: []*typev1.RateLimit{{CommandKind: "Drain", PerMinute: 0}}}},
		{"rate_limits", &typev1.SettingValues{RateLimits: []*typev1.RateLimit{{CommandKind: "nope", PerMinute: 3}}}},
		{"window_name_template", &typev1.SettingValues{WindowNameTemplate: proto.String("a\nb")}},
	}
	for _, tc := range bad {
		a := settings.Merge(settings.Input{Hub: hub(6, tc.v), Prev: prev, Local: &policy.Policy{}}).Ack
		if got := rejected(a); got[tc.field] != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT || len(got) != 1 {
			t.Errorf("%v: rejected %v, want only %s INVALID_ARGUMENT", tc.v, got, tc.field)
		}
		if slices.Contains(a.GetCappedFields(), tc.field) {
			t.Errorf("%v: a rejected field is also capped", tc.v)
		}
		v := a.GetEffective().GetValues()
		if tc.field == "max_active" && v.GetMaxActive() != 3 {
			t.Errorf("%v: max_active %d, want the previous 3", tc.v, v.GetMaxActive())
		}
		// A field the new message left out is no opinion now, not the old value.
		if v.GetMaxWindows() != 12 && tc.field != "max_windows" {
			t.Errorf("%v: max_windows %d, want the default 12", tc.v, v.GetMaxWindows())
		}
		if tc.field == "max_windows" && v.GetMaxWindows() != 7 {
			t.Errorf("max_windows %d, want the previous 7", v.GetMaxWindows())
		}
	}
	// A bad probe interval for one account rejects that entry only.
	h := hub(6, nil)
	h.ProbeIntervalByAccount = map[string]*durationpb.Duration{"acct-1": pd(-time.Minute), "acct-2": pd(time.Hour)}
	a := settings.Merge(settings.Input{Hub: h, Local: &policy.Policy{}}).Ack
	if got := rejected(a); got["probe_interval_by_account.acct-1"] != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT || len(got) != 1 {
		t.Errorf("rejected %v", got)
	}
	if m := a.GetEffective().GetProbeIntervalByAccount(); len(m) != 1 || m["acct-2"].AsDuration() != time.Hour {
		t.Errorf("probe intervals %v", m)
	}
}

// A hub value with no local opinion stands (the file is the ceiling only
// where it says something), except allowed_repo_roots: absent means empty.
func TestAHubValueWithNoLocalOpinionStands(t *testing.T) {
	t.Parallel()
	a := merge(hub(2, &typev1.SettingValues{
		MaxActive: u32(9), RateLimits: []*typev1.RateLimit{{CommandKind: "Drain", PerMinute: 120}},
		AllowedHarnesses: []string{"codex"}, BlockOnBattery: flag(false),
	}), nil)
	v := a.GetEffective().GetValues()
	if v.GetMaxActive() != 9 || rate(v, "Drain") != 120 || !slices.Equal(v.GetAllowedHarnesses(), []string{"codex"}) {
		t.Errorf("effective %v", v)
	}
	if len(a.GetCappedFields()) != 0 {
		t.Errorf("capped %v", a.GetCappedFields())
	}
	for _, f := range []string{"max_active", "rate_limits", "allowed_harnesses"} {
		if l := layer(a, f); l == typev1.SettingLayer_SETTING_LAYER_LOCAL_CEILING || l == typev1.SettingLayer_SETTING_LAYER_AGENT_DEFAULT || l == -1 {
			t.Errorf("%s layer %v, want the hub's", f, l)
		}
	}
	// A local value with no hub opinion is the local layer, not a cap.
	b := merge(hub(2, nil), &policy.Policy{MaxActive: u32(1)})
	if b.GetEffective().GetValues().GetMaxActive() != 1 || len(b.GetCappedFields()) != 0 ||
		layer(b, "max_active") != typev1.SettingLayer_SETTING_LAYER_LOCAL_CEILING {
		t.Errorf("local only: %v capped %v layer %v", b.GetEffective().GetValues().GetMaxActive(), b.GetCappedFields(), layer(b, "max_active"))
	}
}

// In safe mode every hub setting is rejected with POLICY_SAFE_MODE and only
// the built-in defaults are in effect (03e failure table).
func TestSafeModeRejectsEveryHubSetting(t *testing.T) {
	t.Parallel()
	h := hub(8, &typev1.SettingValues{MaxActive: u32(1), BlockOnBattery: flag(true), MergedGrace: pd(time.Hour)})
	h.Labels = []string{"gpu"}
	h.ProbeIntervalByAccount = map[string]*durationpb.Duration{"acct-1": pd(time.Hour)}
	out := settings.Merge(settings.Input{Hub: h, Local: &policy.Policy{}, SafeMode: true})
	got := rejected(out.Ack)
	for _, f := range []string{"max_active", "block_on_battery", "merged_grace", "labels", "probe_interval_by_account.acct-1"} {
		if got[f] != typev1.ReasonCode_REASON_CODE_POLICY_SAFE_MODE {
			t.Errorf("%s not rejected for safe mode: %v", f, got)
		}
	}
	if len(got) != 5 {
		t.Errorf("rejected %v, want exactly the five fields sent", got)
	}
	v := out.Ack.GetEffective().GetValues()
	if v.GetMaxActive() != 2 || v.GetBlockOnBattery() || len(out.Ack.GetEffective().GetLabels()) != 0 {
		t.Errorf("effective in safe mode %v, want the defaults only", v)
	}
	// The hub's values are still kept, so a restart with a fixed file
	// applies them without a re-send.
	if out.Kept.GetValues().GetMaxActive() != 1 || out.Ack.GetGeneration() != 8 {
		t.Errorf("kept %v ack generation %d", out.Kept.GetValues(), out.Ack.GetGeneration())
	}
}

// Field numbers the agent's schema does not know are listed, so the hub can
// say "not enforced on this machine" (03e "Skew with an older agent").
func TestUnrecognizedFieldNumbersAreListed(t *testing.T) {
	t.Parallel()
	vals, err := proto.Marshal(&typev1.SettingValues{MaxActive: u32(3)})
	if err != nil {
		t.Fatal(err)
	}
	vals = protowire.AppendTag(vals, 40, protowire.VarintType)
	vals = protowire.AppendVarint(vals, 1)
	vals = protowire.AppendTag(vals, 41, protowire.BytesType)
	vals = protowire.AppendBytes(vals, []byte("x"))
	vals = protowire.AppendTag(vals, 40, protowire.VarintType) // a repeat counts once
	vals = protowire.AppendVarint(vals, 2)
	var msg []byte
	msg = protowire.AppendTag(msg, 1, protowire.VarintType)
	msg = protowire.AppendVarint(msg, 3)
	msg = protowire.AppendTag(msg, 5, protowire.BytesType)
	msg = protowire.AppendString(msg, "e1")
	msg = protowire.AppendTag(msg, 3, protowire.BytesType)
	msg = protowire.AppendBytes(msg, vals)
	msg = protowire.AppendTag(msg, 30, protowire.Fixed32Type)
	msg = protowire.AppendFixed32(msg, 7)
	var s agentv1.Settings
	if err := proto.Unmarshal(msg, &s); err != nil {
		t.Fatal(err)
	}
	a := merge(&s, nil)
	if !slices.Equal(a.GetUnrecognizedValueFields(), []uint32{40, 41}) || !slices.Equal(a.GetUnrecognizedSettingsFields(), []uint32{30}) {
		t.Errorf("unrecognized values %v settings %v", a.GetUnrecognizedValueFields(), a.GetUnrecognizedSettingsFields())
	}
	if a.GetEffective().GetValues().GetMaxActive() != 3 {
		t.Errorf("the known field was not applied: %v", a.GetEffective().GetValues())
	}
}
