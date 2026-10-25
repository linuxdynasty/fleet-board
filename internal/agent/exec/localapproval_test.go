package exec_test

import (
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestLocalApprovalFileReleasesAWaitingCommand is the L3 acceptance loop
// at the executor level: a DESTRUCTIVE RunHarnessCommand from an operator
// is L! (02g), so it always waits for local approval; the
// AWAITING_LOCAL_CONFIRM progress goes out, `fleet-agent approve` (here
// exec.Decide, the function the CLI calls) answers mid-wait, and the
// handler runs exactly once.
func TestLocalApprovalFileReleasesAWaitingCommand(t *testing.T) {
	t.Parallel()
	h := &handler{
		kind:   "RunHarnessCommand",
		caps:   []*typev1.Capability{{Name: "tmux.control", Version: 1}},
		class:  exec.NotRepeatSafe,
		result: &agentv1.CommandResult{Status: typev1.Status_STATUS_OK, Message: "typed"},
	}
	dir := t.TempDir()
	r := newRig(t)
	r.build(t, exec.FileApprover{Dir: dir}, h)
	r.start(t)
	c := cmd("ap-file-1", 1, "tmux.control")
	c.Kind = &agentv1.Command_RunHarnessCommand{RunHarnessCommand: &agentv1.RunHarnessCommand{
		ExpectedClass: typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE,
	}}
	c.NotAfterTime = timestamppb.New(hub0.Add(2 * time.Minute))
	r.give(t, c)
	// The command must wait first: no result before the person answers.
	select {
	case <-time.After(500 * time.Millisecond):
	case <-r.send.more:
	}
	for _, m := range r.send.all() {
		if res := m.GetCommandResult(); res.GetCommandId().GetValue() == "ap-file-1" {
			t.Fatalf("a DESTRUCTIVE command finished before the local approval: %v", res)
		}
	}
	// The wait announces itself; the person answers with approve.
	waiting := false
	deadline := time.Now().Add(10 * time.Second)
	for !waiting && time.Now().Before(deadline) {
		for _, m := range r.send.all() {
			if p := m.GetCommandProgress(); p.GetCommandId().GetValue() == "ap-file-1" &&
				p.GetStatus() == typev1.Status_STATUS_AWAITING_LOCAL_CONFIRM {
				waiting = true
			}
		}
		if !waiting {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !waiting {
		t.Fatal("no AWAITING_LOCAL_CONFIRM progress for the waiting command")
	}
	if err := exec.Decide(dir, "ap-file-1", true); err != nil {
		t.Fatal(err)
	}
	if res := r.result(t, "ap-file-1"); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("result = %v, want OK after approve", res)
	}
	if got := h.runs.Load(); got != 1 {
		t.Fatalf("handler runs = %d, want exactly 1 after approve", got)
	}
}
