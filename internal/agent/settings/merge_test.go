package settings_test

import (
	"slices"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/settings"
)

func u32(v uint32) *uint32                    { return &v }
func u64(v uint64) *uint64                    { return &v }
func dur(d time.Duration) *time.Duration      { return &d }
func flag(b bool) *bool                       { return &b }
func pd(d time.Duration) *durationpb.Duration { return durationpb.New(d) }

func hub(gen uint64, v *typev1.SettingValues) *agentv1.Settings {
	return &agentv1.Settings{Generation: gen, SettingsEpoch: "e1", Values: v}
}

func merge(h *agentv1.Settings, local *policy.Policy) *agentv1.SettingsAck {
	if local == nil {
		local = &policy.Policy{}
	}
	return settings.Merge(settings.Input{Hub: h, Local: local}).Ack
}

// layer is the layer the ack names for field ("" with no source).
func layer(a *agentv1.SettingsAck, field string) typev1.SettingLayer {
	for _, s := range a.GetEffective().GetSources() {
		if s.GetField() == field {
			return s.GetLayer()
		}
	}
	return -1
}

// The tighten-only table of 03e, one row per field, with the hub looser than
// the local ceiling and then stricter: the local value wins only when it is
// stricter, and only then is the field capped.
func TestMergeTightensEveryFieldInTheRightDirection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		field  string
		hub    *typev1.SettingValues
		local  policy.Policy
		check  func(v *typev1.SettingValues) bool
		capped bool
	}{
		{
			"max_active hub looser", "max_active", &typev1.SettingValues{MaxActive: u32(8)},
			policy.Policy{MaxActive: u32(3)},
			func(v *typev1.SettingValues) bool { return v.GetMaxActive() == 3 }, true,
		},
		{
			"max_active hub stricter", "max_active", &typev1.SettingValues{MaxActive: u32(2)},
			policy.Policy{MaxActive: u32(3)},
			func(v *typev1.SettingValues) bool { return v.GetMaxActive() == 2 }, false,
		},
		{
			"max_total hub looser", "max_total", &typev1.SettingValues{MaxTotal: u32(20)},
			policy.Policy{MaxTotal: u32(5)},
			func(v *typev1.SettingValues) bool { return v.GetMaxTotal() == 5 }, true,
		},
		{
			"max_total hub stricter", "max_total", &typev1.SettingValues{MaxTotal: u32(4)},
			policy.Policy{MaxTotal: u32(5)},
			func(v *typev1.SettingValues) bool { return v.GetMaxTotal() == 4 }, false,
		},
		{
			"max_windows hub looser", "max_windows", &typev1.SettingValues{MaxWindows: u32(40)},
			policy.Policy{MaxWindows: u32(10)},
			func(v *typev1.SettingValues) bool { return v.GetMaxWindows() == 10 }, true,
		},
		{
			"max_windows equal is not a cap", "max_windows", &typev1.SettingValues{MaxWindows: u32(10)},
			policy.Policy{MaxWindows: u32(10)},
			func(v *typev1.SettingValues) bool { return v.GetMaxWindows() == 10 }, false,
		},
		{
			"cert_lifetime hub looser", "cert_lifetime", &typev1.SettingValues{CertLifetime: pd(200 * 24 * time.Hour)},
			policy.Policy{CertLifetime: dur(30 * 24 * time.Hour)},
			func(v *typev1.SettingValues) bool { return v.GetCertLifetime().AsDuration() == 30*24*time.Hour }, true,
		},
		{
			"cert_lifetime hub stricter", "cert_lifetime", &typev1.SettingValues{CertLifetime: pd(7 * 24 * time.Hour)},
			policy.Policy{CertLifetime: dur(30 * 24 * time.Hour)},
			func(v *typev1.SettingValues) bool { return v.GetCertLifetime().AsDuration() == 7*24*time.Hour }, false,
		},
		{
			"attach_idle_timeout hub looser", "attach_idle_timeout", &typev1.SettingValues{AttachIdleTimeout: pd(time.Hour)},
			policy.Policy{AttachIdleTimeout: dur(10 * time.Minute)},
			func(v *typev1.SettingValues) bool { return v.GetAttachIdleTimeout().AsDuration() == 10*time.Minute }, true,
		},
		{
			"attach_idle_timeout hub stricter", "attach_idle_timeout", &typev1.SettingValues{AttachIdleTimeout: pd(time.Minute)},
			policy.Policy{AttachIdleTimeout: dur(10 * time.Minute)},
			func(v *typev1.SettingValues) bool { return v.GetAttachIdleTimeout().AsDuration() == time.Minute }, false,
		},
		{
			"spool_cap_bytes hub looser", "spool_cap_bytes", &typev1.SettingValues{SpoolCapBytes: u64(1 << 40)},
			policy.Policy{SpoolCapBytes: u64(1 << 20)},
			func(v *typev1.SettingValues) bool { return v.GetSpoolCapBytes() == 1<<20 }, true,
		},
		{
			"spool_cap_bytes hub stricter", "spool_cap_bytes", &typev1.SettingValues{SpoolCapBytes: u64(1 << 10)},
			policy.Policy{SpoolCapBytes: u64(1 << 20)},
			func(v *typev1.SettingValues) bool { return v.GetSpoolCapBytes() == 1<<10 }, false,
		},
		{
			"block_on_battery hub off, local on", "block_on_battery", &typev1.SettingValues{BlockOnBattery: flag(false)},
			policy.Policy{BlockOnBattery: flag(true)},
			func(v *typev1.SettingValues) bool { return v.GetBlockOnBattery() }, true,
		},
		{
			"block_on_battery hub on, local off", "block_on_battery", &typev1.SettingValues{BlockOnBattery: flag(true)},
			policy.Policy{BlockOnBattery: flag(false)},
			func(v *typev1.SettingValues) bool { return v.GetBlockOnBattery() }, false,
		},
		{
			"default_server hub looser", "default_server", &typev1.SettingValues{DefaultServer: typev1.DefaultServerMode_DEFAULT_SERVER_MODE_CONTROL.Enum()},
			policy.Policy{DefaultServer: ptr(policy.ServerObserve)},
			func(v *typev1.SettingValues) bool {
				return v.GetDefaultServer() == typev1.DefaultServerMode_DEFAULT_SERVER_MODE_OBSERVE
			}, true,
		},
		{
			"default_server hub stricter", "default_server", &typev1.SettingValues{DefaultServer: typev1.DefaultServerMode_DEFAULT_SERVER_MODE_NONE.Enum()},
			policy.Policy{DefaultServer: ptr(policy.ServerObserve)},
			func(v *typev1.SettingValues) bool {
				return v.GetDefaultServer() == typev1.DefaultServerMode_DEFAULT_SERVER_MODE_NONE
			}, false,
		},
		{
			"probe_interval is a floor: hub shorter", "probe_interval", &typev1.SettingValues{ProbeInterval: pd(5 * time.Minute)},
			policy.Policy{MinProbeInterval: dur(20 * time.Minute)},
			func(v *typev1.SettingValues) bool { return v.GetProbeInterval().AsDuration() == 20*time.Minute }, true,
		},
		{
			"probe_interval is a floor: hub longer", "probe_interval", &typev1.SettingValues{ProbeInterval: pd(time.Hour)},
			policy.Policy{MinProbeInterval: dur(20 * time.Minute)},
			func(v *typev1.SettingValues) bool { return v.GetProbeInterval().AsDuration() == time.Hour }, false,
		},
		{
			"allowed_harnesses is the intersection", "allowed_harnesses", &typev1.SettingValues{AllowedHarnesses: []string{"claude", "codex", "muse"}},
			policy.Policy{AllowedHarnesses: []string{"codex"}},
			func(v *typev1.SettingValues) bool { return slices.Equal(v.GetAllowedHarnesses(), []string{"codex"}) }, true,
		},
		{
			"allowed_harnesses hub inside local", "allowed_harnesses", &typev1.SettingValues{AllowedHarnesses: []string{"codex"}},
			policy.Policy{AllowedHarnesses: []string{"codex", "muse"}},
			func(v *typev1.SettingValues) bool { return slices.Equal(v.GetAllowedHarnesses(), []string{"codex"}) }, false,
		},
		{
			"allowed_repo_roots is the intersection", "allowed_repo_roots", &typev1.SettingValues{AllowedRepoRoots: []string{"/r/a", "/r/b"}},
			policy.Policy{AllowedRepoRoots: []string{"/r/b"}},
			func(v *typev1.SettingValues) bool { return slices.Equal(v.GetAllowedRepoRoots(), []string{"/r/b"}) }, true,
		},
		{
			"allowed_repo_roots: a sub-folder of a local root is not a match", "allowed_repo_roots", &typev1.SettingValues{AllowedRepoRoots: []string{"/r/b/x"}},
			policy.Policy{AllowedRepoRoots: []string{"/r/b"}},
			func(v *typev1.SettingValues) bool { return len(v.GetAllowedRepoRoots()) == 0 }, true,
		},
		{
			"allowed_repo_roots: no local opinion is empty, so the hub adds none", "allowed_repo_roots", &typev1.SettingValues{AllowedRepoRoots: []string{"/r/a"}},
			policy.Policy{},
			func(v *typev1.SettingValues) bool { return len(v.GetAllowedRepoRoots()) == 0 }, true,
		},
		{
			"rate_limits min per kind", "rate_limits", &typev1.SettingValues{RateLimits: []*typev1.RateLimit{{CommandKind: "SendKeys", PerMinute: 100}, {CommandKind: "Drain", PerMinute: 5}}},
			policy.Policy{RateLimits: []policy.RateLimit{{CommandKind: "SendKeys", PerMinute: 10}, {CommandKind: "Drain", PerMinute: 50}}},
			func(v *typev1.SettingValues) bool { return rate(v, "SendKeys") == 10 && rate(v, "Drain") == 5 }, true,
		},
		{
			"rate_limits hub stricter everywhere", "rate_limits", &typev1.SettingValues{RateLimits: []*typev1.RateLimit{{CommandKind: "SendKeys", PerMinute: 3}}},
			policy.Policy{RateLimits: []policy.RateLimit{{CommandKind: "SendKeys", PerMinute: 10}}},
			func(v *typev1.SettingValues) bool { return rate(v, "SendKeys") == 3 }, false,
		},
		{
			"local_approval OR per kind", "local_approval", &typev1.SettingValues{LocalApproval: []*typev1.LocalApprovalRule{{CommandKind: "KillWindow", Required: false}, {CommandKind: "Drain", Required: true}}},
			policy.Policy{LocalApproval: []policy.ApprovalRule{{CommandKind: "KillWindow", Required: true}, {CommandKind: "Drain", Required: false}}},
			func(v *typev1.SettingValues) bool { return approval(v, "KillWindow") && approval(v, "Drain") }, true,
		},
		{
			"local_approval hub requires more", "local_approval", &typev1.SettingValues{LocalApproval: []*typev1.LocalApprovalRule{{CommandKind: "Drain", Required: true}}},
			policy.Policy{LocalApproval: []policy.ApprovalRule{{CommandKind: "Drain", Required: false}}},
			func(v *typev1.SettingValues) bool { return approval(v, "Drain") }, false,
		},
		{
			"idle_threshold: the hub wins", "idle_threshold", &typev1.SettingValues{IdleThreshold: pd(9 * time.Minute)},
			policy.Policy{IdleThreshold: dur(time.Minute)},
			func(v *typev1.SettingValues) bool { return v.GetIdleThreshold().AsDuration() == 9*time.Minute }, false,
		},
		{
			"share_identity_email: the hub wins", "share_identity_email", &typev1.SettingValues{ShareIdentityEmail: flag(true)},
			policy.Policy{ShareIdentityEmail: flag(false)},
			func(v *typev1.SettingValues) bool { return v.GetShareIdentityEmail() }, false,
		},
		{
			"window_name_template: the hub wins", "window_name_template", &typev1.SettingValues{WindowNameTemplate: proto.String("{work}")},
			policy.Policy{WindowNameTemplate: proto.String("x")},
			func(v *typev1.SettingValues) bool { return v.GetWindowNameTemplate() == "{work}" }, false,
		},
		{
			"merged_grace is echoed unchanged", "merged_grace", &typev1.SettingValues{MergedGrace: pd(3 * time.Hour)},
			policy.Policy{},
			func(v *typev1.SettingValues) bool { return v.GetMergedGrace().AsDuration() == 3*time.Hour }, false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := merge(hub(13, tc.hub), &tc.local)
			if a.GetGeneration() != 13 || a.GetSettingsEpoch() != "e1" || a.GetEffective().GetGeneration() != 13 {
				t.Errorf("ack generation %d epoch %q effective %d", a.GetGeneration(), a.GetSettingsEpoch(), a.GetEffective().GetGeneration())
			}
			if !tc.check(a.GetEffective().GetValues()) {
				t.Errorf("effective %v", a.GetEffective().GetValues())
			}
			if got := slices.Contains(a.GetCappedFields(), tc.field); got != tc.capped {
				t.Errorf("capped_fields %v, %s capped = %v, want %v", a.GetCappedFields(), tc.field, got, tc.capped)
			}
			if tc.capped && layer(a, tc.field) != typev1.SettingLayer_SETTING_LAYER_LOCAL_CEILING {
				t.Errorf("%s layer %v, want LOCAL_CEILING", tc.field, layer(a, tc.field))
			}
			if len(a.GetRejected()) != 0 {
				t.Errorf("rejected %v", a.GetRejected())
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func rate(v *typev1.SettingValues, kind string) uint32 {
	for _, r := range v.GetRateLimits() {
		if r.GetCommandKind() == kind {
			return r.GetPerMinute()
		}
	}
	return 0
}

func approval(v *typev1.SettingValues, kind string) bool {
	for _, r := range v.GetLocalApproval() {
		if r.GetCommandKind() == kind {
			return r.GetRequired()
		}
	}
	return false
}

// With no layer stating a field, the built-in default of defaults.md is in
// effect and named AGENT_DEFAULT; allowed_harnesses with no opinion anywhere
// is every name the contract knows, with no source (ruling row 6 fork 6).
func TestMergeFallsBackToTheBuiltInDefaults(t *testing.T) {
	t.Parallel()
	a := merge(hub(1, nil), nil)
	v := a.GetEffective().GetValues()
	if v.GetMaxActive() != 2 || v.GetMaxTotal() != 4 || v.GetMaxWindows() != 12 ||
		v.GetCertLifetime().AsDuration() != 90*24*time.Hour || v.GetAttachIdleTimeout().AsDuration() != 30*time.Minute ||
		v.GetSpoolCapBytes() != 2<<30 || v.GetIdleThreshold().AsDuration() != 2*time.Minute ||
		v.GetWindowNameTemplate() != "{harness} {work}" || v.BlockOnBattery == nil || v.GetBlockOnBattery() ||
		v.ShareIdentityEmail == nil || v.GetShareIdentityEmail() ||
		v.GetDefaultServer() != typev1.DefaultServerMode_DEFAULT_SERVER_MODE_CONTROL {
		t.Errorf("defaults %v", v)
	}
	if layer(a, "max_active") != typev1.SettingLayer_SETTING_LAYER_AGENT_DEFAULT {
		t.Errorf("max_active layer %v, want AGENT_DEFAULT", layer(a, "max_active"))
	}
	if want := []string{"claude", "codex", "muse", "cursor", "opencode"}; !slices.Equal(v.GetAllowedHarnesses(), want) {
		t.Errorf("allowed_harnesses %v, want %v", v.GetAllowedHarnesses(), want)
	}
	if layer(a, "allowed_harnesses") != -1 {
		t.Errorf("allowed_harnesses has a source with no layer stating it")
	}
	if len(v.GetAllowedRepoRoots()) != 0 || v.GetProbeInterval() != nil || v.GetMergedGrace() != nil || len(a.GetCappedFields()) != 0 {
		t.Errorf("no opinion: roots %v probe %v grace %v capped %v", v.GetAllowedRepoRoots(), v.GetProbeInterval(), v.GetMergedGrace(), a.GetCappedFields())
	}
	// An explicit empty list in the file is an opinion: no harness.
	b := merge(hub(1, &typev1.SettingValues{AllowedHarnesses: []string{"codex"}}), &policy.Policy{AllowedHarnesses: []string{}})
	if got := b.GetEffective().GetValues().GetAllowedHarnesses(); len(got) != 0 {
		t.Errorf("allowed_harnesses under an empty local list %v, want none", got)
	}
}

// Labels: the hub's plus the file's; the file cannot remove a hub label.
// Probe intervals per account are floored at the local minimum.
func TestMergeLabelsAndProbeIntervalsPerAccount(t *testing.T) {
	t.Parallel()
	h := hub(4, nil)
	h.Labels = []string{"laptop", "gpu"}
	h.ProbeIntervalByAccount = map[string]*durationpb.Duration{"acct-1": pd(time.Minute), "acct-2": pd(time.Hour)}
	a := merge(h, &policy.Policy{Labels: []string{"home", "gpu"}, MinProbeInterval: dur(15 * time.Minute)})
	if got := a.GetEffective().GetLabels(); !slices.Equal(got, []string{"gpu", "home", "laptop"}) {
		t.Errorf("labels %v", got)
	}
	m := a.GetEffective().GetProbeIntervalByAccount()
	if m["acct-1"].AsDuration() != 15*time.Minute || m["acct-2"].AsDuration() != time.Hour || len(m) != 2 {
		t.Errorf("probe intervals %v", m)
	}
	if !slices.Contains(a.GetCappedFields(), "probe_interval") {
		t.Errorf("capped %v, want probe_interval", a.GetCappedFields())
	}
}
