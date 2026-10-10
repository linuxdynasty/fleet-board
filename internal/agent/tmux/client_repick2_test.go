package tmux_test

import (
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// tmux can move the control client to another session by itself (with
// detach-on-destroy off, when its session ends). A %session-changed the
// client did not ask for makes it re-pick: moved to an internal session
// while a developer one exists, it switches back.
func TestAnUnexpectedSessionChangeRepicks(t *testing.T) {
	t.Setenv("FAKE_CTL_LIST", `$0 \n$2 attach_view\n`)
	r := startFake(t, &backoffs{wait: time.Hour}, nil)
	r.waitState(t, tmux.StateUp)
	if _, err := r.Capture(ctxT(t), 6); err != nil { // tmux then moves the client to $2
		t.Fatal(err)
	}
	waitTrue(t, "a re-pick back to $0", func() bool { return count(commands(t, r), "switch-client -E -t $0") == 1 })
	waitTrue(t, "Session $0", func() bool { return r.Session() == 0 })
}

// Moved by tmux to another developer session, the client lists and stays.
func TestAnUnexpectedMoveToADeveloperSessionStays(t *testing.T) {
	t.Setenv("FAKE_CTL_LIST", `$0 \n$2 \n`)
	r := startFake(t, &backoffs{wait: time.Hour}, nil)
	r.waitState(t, tmux.StateUp)
	if _, err := r.Capture(ctxT(t), 6); err != nil {
		t.Fatal(err)
	}
	waitTrue(t, "a list after the move", func() bool { return count(commands(t, r), "list-sessions") == 1 })
	if _, err := r.Capture(ctxT(t), 3); err != nil { // a round trip after the re-pick
		t.Fatal(err)
	}
	if got := commands(t, r); count(got, "switch-client") != 0 || r.Session() != 2 {
		t.Fatalf("commands %q, session %s; want no switch and $2", got, r.Session())
	}
}

// A list line the client cannot read is a failed re-pick, logged.
func TestAMalformedListIsARepickFailure(t *testing.T) {
	t.Setenv("FAKE_PROC_LIST", `$0 attach_view\n`)
	t.Setenv("FAKE_CTL_LIST", `garbage\n`)
	r := startFake(t, &backoffs{wait: time.Hour}, nil)
	r.waitState(t, tmux.StateUp)
	waitEvent(t, r.logs, "tmux.repick_failed", map[string]any{"err_kind": "list"})
}

// The conn ending while a re-pick waits is the attach ending, not a failed
// re-pick: no tmux.repick_failed.
func TestAConnEndingDuringARepickLogsNoRepickFailure(t *testing.T) {
	t.Setenv("FAKE_PROC_LIST", `$0 attach_view\n`)
	t.Setenv("FAKE_LIST_EXIT", "1")
	r := startFake(t, &backoffs{wait: time.Hour}, nil)
	waitEvent(t, r.logs, "tmux.exit", map[string]any{"reason": "eof"})
	time.Sleep(200 * time.Millisecond) // anything the re-pick logs lands by now
	for _, ev := range r.logs.events(t) {
		if ev["msg"] == "tmux.repick_failed" {
			t.Fatalf("tmux.repick_failed logged for a conn that ended: %v", ev)
		}
	}
}

// A caller's context ending while its write is blocked ends the attach as
// write_canceled, told apart from tmux not reading (write_stalled).
func TestACanceledWriteHasItsOwnReason(t *testing.T) {
	t.Setenv("FAKE_STALL", "1")
	r := startFake(t, &backoffs{wait: time.Hour}, func(c *tmux.Client) { tmux.SetWriteTimeoutForTest(c, time.Hour) })
	r.waitState(t, tmux.StateUp)
	big := literal(t, fakeKey(), bigText())
	ctx, cancel := contextWithTimeout(time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { // 160 KB: more than the pipe holds, so a write blocks
		defer close(done)
		for range 40 {
			go func() { _ = r.Send(ctx, big) }()
		}
	}()
	waitEvent(t, r.logs, "tmux.exit", map[string]any{"reason": "write_canceled"})
	<-done
}
