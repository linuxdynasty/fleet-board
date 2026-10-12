package settings_test

import (
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/agent/settings"
)

// The production types fill the seams, and the Manager is the executor's
// policy (plan row 6i wires them).
var (
	_ settings.Store  = (*ring.DB)(nil)
	_ settings.Local  = (*policy.Manager)(nil)
	_ settings.Sender = (*conn.Loop)(nil)
	_ exec.Policy     = (*settings.Manager)(nil)
)

// Every source, cap and rejection names a field of SettingValues or of
// Settings, as the hub knows them.
func TestTheNamesInAnAckAreProtoFieldNames(t *testing.T) {
	t.Parallel()
	vals := (&typev1.SettingValues{}).ProtoReflect().Descriptor().Fields()
	top := (&agentv1.Settings{}).ProtoReflect().Descriptor().Fields()
	h := hub(3, &typev1.SettingValues{
		MaxActive: u32(9), MaxTotal: u32(9), MaxWindows: u32(9), CertLifetime: pd(1e15), AttachIdleTimeout: pd(1e12),
		SpoolCapBytes: u64(9), BlockOnBattery: flag(false), ProbeInterval: pd(1e9), MergedGrace: pd(1e9),
		AllowedHarnesses: []string{"codex"}, AllowedRepoRoots: []string{"/r"},
		LocalApproval: []*typev1.LocalApprovalRule{{CommandKind: "Drain", Required: false}},
		RateLimits:    []*typev1.RateLimit{{CommandKind: "Drain", PerMinute: 9}},
		DefaultServer: typev1.DefaultServerMode_DEFAULT_SERVER_MODE_CONTROL.Enum(), IdleThreshold: pd(1e9),
		WindowNameTemplate: ptr("x"), ShareIdentityEmail: flag(true),
	})
	l := &policy.Policy{
		MaxActive: u32(1), MaxTotal: u32(1), MaxWindows: u32(1), CertLifetime: dur(1), AttachIdleTimeout: dur(1),
		SpoolCapBytes: u64(1), BlockOnBattery: flag(true), MinProbeInterval: dur(1e12), AllowedHarnesses: []string{},
		AllowedRepoRoots: []string{}, LocalApproval: []policy.ApprovalRule{{CommandKind: "Drain", Required: true}},
		RateLimits: []policy.RateLimit{{CommandKind: "Drain", PerMinute: 1}}, DefaultServer: ptr(policy.ServerNone),
	}
	a := merge(h, l)
	names := a.GetCappedFields()
	for _, s := range a.GetEffective().GetSources() {
		names = append(names, s.GetField())
	}
	if len(a.GetCappedFields()) != 13 {
		t.Errorf("capped %v, want the 13 safety fields", a.GetCappedFields())
	}
	for _, n := range names {
		if vals.ByTextName(n) == nil && top.ByTextName(n) == nil {
			t.Errorf("%q is not a field name", n)
		}
	}
}
