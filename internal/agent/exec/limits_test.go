package exec_test

import (
	"context"
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// fillUnit shrinks about 25 times under redaction: password=<500 bytes>
// becomes password=[redacted].
var fillUnit = "password=" + strings.Repeat("p", 500)

// oneLine is n bytes of shrinking filler on one line (no newline).
func oneLine(n int) string {
	var b strings.Builder
	for b.Len()+len(fillUnit)+1 <= n {
		b.WriteString(fillUnit + " ")
	}
	b.WriteString(strings.Repeat(" ", n-b.Len()))
	return b.String()
}

// manyLines is n bytes of shrinking filler, one unit per line, ending in a
// newline, so the text after it starts a line of its own.
func manyLines(n int) string {
	var b strings.Builder
	for b.Len()+len(fillUnit)+1 <= n-1 {
		b.WriteString(fillUnit + "\n")
	}
	b.WriteString(strings.Repeat(" ", n-b.Len()-1) + "\n")
	return b.String()
}

// 6e round-2 ruling C: a handler's text is cut back to a line boundary at
// or before 8 KiB before the scrub, then to 1 KiB by the scrubber. A secret
// that straddles the 8 KiB point leaves no byte of its cut head, nor of
// what followed it, in the result sent or logged in agent.db; with filler
// that shrinks under redaction the head would otherwise land inside the
// final 1 KiB. Rows with the filler on separate lines keep the filler
// (redacted) and drop the secret's line whole, so an empty result is not
// the only thing that passes.
func TestASecretAcrossThePreCutNeverLeaves(t *testing.T) {
	t.Parallel()
	const (
		aws   = "AKIAABCDEFGHIJKLMNOP"
		slack = "xoxb" + "-123456789012-1234567890123-abcdefghijklmnopqrstuvwx"
		gh    = "ghp_QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
		ant   = "sk-ant-api03-Ab12Cd34Ef56Gh78Ij90-xyz"
	)
	for _, tc := range []struct {
		name   string
		fill   func(int) string
		secret string
		keep   int // bytes of the secret before the 8 KiB point
		kept   bool
	}{
		{"x padding", func(n int) string { return strings.Repeat("x", n) }, gh, 20, false},
		{"aws key, one line", oneLine, aws, 12, false},
		{"slack token, one line", oneLine, slack, 25, false},
		{"github token, one line", oneLine, gh, 12, false},
		{"anthropic key, one line", oneLine, ant, 8, false},
		{"aws key, many lines", manyLines, aws, 12, true},
		{"slack token, many lines", manyLines, slack, 25, true},
		{"github token, many lines", manyLines, gh, 12, true},
		{"anthropic key, many lines", manyLines, ant, 8, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			head := tc.secret[:tc.keep]
			msg := tc.fill(8<<10-tc.keep) + tc.secret + " tail"
			if !strings.HasPrefix(msg[8<<10-tc.keep:], head) || len(msg[:8<<10]) != 8<<10 {
				t.Fatal("the secret does not straddle the 8 KiB point")
			}
			h := capturePane("tmux.observe")
			h.result = &agentv1.CommandResult{Status: typev1.Status_STATUS_OK, Message: msg}
			r := newRig(t, h)
			r.start(t)
			r.give(t, cmd("p-1", 1, "tmux.observe"))
			out := r.result(t, "p-1").GetMessage()
			if len(out) > 1024 || strings.Contains(out, head) || strings.Contains(out, "tail") {
				t.Errorf("message of %d bytes holds the secret's head %q or what followed it; it ends ...%q",
					len(out), head, out[max(0, len(out)-40):])
			}
			if got := strings.Contains(out, "password=[redacted]"); got != tc.kept {
				t.Errorf("message keeps the filler: %v, want %v", got, tc.kept)
			}
			e, err := r.db.Command(context.Background(), "p-1")
			if err != nil || strings.Contains(string(e.Result), head) || strings.Contains(string(e.Result), "tail") {
				t.Errorf("the result logged in agent.db holds the secret's head (err %v)", err)
			}
		})
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
