package exec_test

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// A denial and a confirm timeout are refusals by the executor's own
// approval step, not handler results: a command.refused line, the refused
// counters, and no timing (05b; 02g audit).
func TestAnApprovalRefusalIsARefusal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		a      exec.Approver
		reason string
		want   typev1.ReasonCode
	}{
		{"denied", &approver{decide: false}, "LOCAL_DENIED", typev1.ReasonCode_REASON_CODE_LOCAL_DENIED},
		{"timed out", nil, "LOCAL_CONFIRM_TIMEOUT", typev1.ReasonCode_REASON_CODE_LOCAL_CONFIRM_TIMEOUT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &handler{kind: "Interrupt", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}}
			r := newRig(t)
			r.build(t, tc.a, h)
			r.pol.approval["Interrupt"] = true
			r.start(t)
			c := cmd("ap-1", 1, "tmux.control")
			c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
			c.NotAfterTime = timestamppb.New(hub0.Add(2 * time.Second))
			r.give(t, c)
			if res := r.result(t, "ap-1"); res.GetReason() != tc.want {
				t.Fatalf("result %v, want %v", res, tc.want)
			}
			refused := conntest.Lines(t, r.logs.String(), "command.refused")
			if len(refused) != 1 || refused[0]["reason"] != tc.reason {
				t.Errorf("command.refused lines %v, want one with reason %s", refused, tc.reason)
			}
			if got := conntest.Lines(t, r.logs.String(), "command.finished"); len(got) != 0 {
				t.Errorf("command.finished lines %v, want none for a refusal", got)
			}
			if got := r.metric(t, "fleet_agent_commands_refused_total", `reason="`+tc.reason+`"`); got != "1" {
				t.Errorf("commands_refused_total{reason=%s} %q, want 1", tc.reason, got)
			}
			if got := r.ex.RefusedTotal(); got != 1 {
				t.Errorf("RefusedTotal %d, want 1", got)
			}
			if got := r.metric(t, "fleet_agent_command_seconds_count", `kind="Interrupt"`); got != "" {
				t.Errorf("command_seconds{kind=Interrupt} count %q, want no sample for a refusal", got)
			}
		})
	}
}
