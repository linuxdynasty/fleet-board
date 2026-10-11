package exec_test

import (
	"context"
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// A handler's text is cut to 8 KiB before the scrub and to 1 KiB after it
// (02e). A secret that straddles the 8 KiB cut never leaves the machine:
// its head, which the scrubber may not recognise once cut, lies past the
// final 1 KiB.
func TestASecretAcrossThePreCutNeverLeaves(t *testing.T) {
	t.Parallel()
	token := "ghp_" + strings.Repeat("Q", 36)
	msg := strings.Repeat("x", 8<<10-20) + token + " tail"
	h := capturePane("tmux.observe")
	h.result = &agentv1.CommandResult{Status: typev1.Status_STATUS_OK, Message: msg}
	r := newRig(t, h)
	r.start(t)
	r.give(t, cmd("p-1", 1, "tmux.observe"))
	out := r.result(t, "p-1").GetMessage()
	if len(out) > 1024 || strings.Contains(out, "Q") || strings.Contains(out, "ghp_") || strings.Contains(out, "tail") {
		t.Errorf("message of %d bytes holds part of the secret or what followed it", len(out))
	}
	e, err := r.db.Command(context.Background(), "p-1")
	if err != nil || strings.Contains(string(e.Result), "QQQQ") {
		t.Errorf("the logged result holds the secret (err %v)", err)
	}
}

// The hub always sets not_after_time; a command without one is past its
// deadline: logged EXPIRED and its seq not applied.
func TestACommandWithNoDeadlineIsExpired(t *testing.T) {
	t.Parallel()
	h := capturePane("tmux.observe")
	r := newRig(t, h)
	r.start(t)
	c := cmd("d-1", 4, "tmux.observe")
	c.NotAfterTime = nil
	r.give(t, c)
	if res := r.result(t, "d-1"); res.GetStatus() != typev1.Status_STATUS_EXPIRED || h.runs.Load() != 0 {
		t.Errorf("result %v after %d runs, want EXPIRED, never run", res, h.runs.Load())
	}
	if e, err := r.db.Command(context.Background(), "d-1"); err != nil || e.Status != ring.StatusFinished {
		t.Errorf("entry %+v err %v, want logged and finished", e, err)
	}
	if st, err := r.db.State(context.Background()); err != nil || st.CommandSeq != 0 {
		t.Errorf("command seq %d (err %v), want 0: an expired command's seq is not applied", st.CommandSeq, err)
	}
}

// RefusedTotal counts results the executor's own checks gave, logged or
// not; a handler's result is not one (AgentHealth.commands_refused_total).
func TestRefusedTotal(t *testing.T) {
	t.Parallel()
	r := newRig(t, capturePane("tmux.observe"))
	r.start(t)
	r.give(t, cmd("u-1", 1, "tmux.control")) // UNSUPPORTED
	r.result(t, "u-1")
	c := cmd("u-2", 2, "tmux.observe")
	c.Epoch = 9 // EPOCH_MISMATCH, unlogged
	r.give(t, c)
	r.result(t, "u-2")
	r.give(t, cmd("u-3", 3, "tmux.observe")) // runs
	r.result(t, "u-3")
	if got := r.ex.RefusedTotal(); got != 2 {
		t.Errorf("RefusedTotal = %d, want 2", got)
	}
}
