package exec_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// A KillWindow whose handler cannot say which window it is (no Variant) is
// judged as a developer's window, the strictest row (02g legend): refused
// for an OPERATOR and the SCHEDULER, and an OWNER's waits for local
// approval (the cell is L and the test's rule covers KillWindow).
func TestKillWindowWithNoVariantIsADevelopersWindow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		role typev1.Role
		want typev1.ReasonCode // UNSPECIFIED: it waits
	}{
		{typev1.Role_ROLE_OPERATOR, typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT},
		{typev1.Role_ROLE_SCHEDULER, typev1.ReasonCode_REASON_CODE_ROLE_INSUFFICIENT},
		{typev1.Role_ROLE_OWNER, typev1.ReasonCode_REASON_CODE_UNSPECIFIED},
	} {
		t.Run(tc.role.String(), func(t *testing.T) {
			t.Parallel()
			h := &handler{kind: "KillWindow", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}}
			r := newRig(t, h)
			r.pol.approval["KillWindow"] = true
			r.start(t)
			c := cmd("kw", 1, "tmux.control")
			c.RequestedBy.Role = tc.role
			c.Kind = &agentv1.Command_KillWindow{KillWindow: &agentv1.KillWindow{}}
			c.NotAfterTime = timestamppb.New(hub0.Add(time.Hour))
			r.give(t, c)
			if tc.want != typev1.ReasonCode_REASON_CODE_UNSPECIFIED {
				if res := r.result(t, "kw"); res.GetReason() != tc.want {
					t.Errorf("result %v, want %v", res, tc.want)
				}
				return
			}
			waitFor(t, func() bool { _, p, _ := r.count("kw"); return p == 1 })
			if _, _, n := r.count("kw"); n != 0 || h.runs.Load() != 0 {
				t.Errorf("an OWNER's KillWindow ran or was answered (%d results) before an approval", n)
			}
		})
	}
}

// Safe mode lets only observe commands through, whatever the role (02g,
// "observe command"): an OWNER's SetLogCapture is refused, an OWNER's
// CapturePane runs.
func TestSafeModeRefusesAnOwnersControlCommand(t *testing.T) {
	t.Parallel()
	logs := &handler{kind: "SetLogCapture", caps: []*typev1.Capability{{Name: "logs.terminal", Version: 1}}}
	capture := capturePane("tmux.observe")
	r := newRig(t, logs, capture)
	r.pol.safe.Store(true)
	r.start(t)
	c := cmd("sm-1", 1, "logs.terminal")
	c.RequestedBy.Role = typev1.Role_ROLE_OWNER
	c.Kind = &agentv1.Command_SetLogCapture{SetLogCapture: &agentv1.SetLogCapture{}}
	r.give(t, c)
	if res := r.result(t, "sm-1"); res.GetReason() != typev1.ReasonCode_REASON_CODE_POLICY_SAFE_MODE || logs.runs.Load() != 0 {
		t.Errorf("SetLogCapture in safe mode: %v after %d runs, want POLICY_SAFE_MODE", res, logs.runs.Load())
	}
	c = cmd("sm-2", 2, "tmux.observe")
	c.RequestedBy.Role = typev1.Role_ROLE_OWNER
	r.give(t, c)
	if res := r.result(t, "sm-2"); res.GetStatus() != typev1.Status_STATUS_OK || capture.runs.Load() != 1 {
		t.Errorf("CapturePane in safe mode: %v after %d runs, want OK", res, capture.runs.Load())
	}
}
