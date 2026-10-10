package tmux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// fakeTmux is a shell script that answers like a tmux control client, so a
// test can show what the client does with a release that lacks the flags or
// with replies real tmux writes too rarely to wait for (%pause, spike 0.5
// finding 7). It records each control-mode command line in $dir/commands.
// FAKE_LIST_ERR makes list-sessions fail with that text on stderr;
// FAKE_FAIL_ATTACHES=N makes the first N attaches end at once; FAKE_STALL=1
// makes it stop reading its input after the handshake. FAKE_PROC_LIST and
// FAKE_CTL_LIST are what list-sessions prints as a one-shot command and
// through the control client (printf %b); FAKE_LIST_SLEEP slows the
// latter; FAKE_SWITCH_ERR refuses switch-client. "capture-pane -p -t %7"
// is answered, then five %sessions-changed follow.
const fakeTmux = `#!/bin/sh
dir=$(dirname "$0")
if [ "$1" = "-V" ]; then echo "tmux $FAKE_VERSION"; exit 0; fi
shift 2
case "$1" in
list-sessions)
	if [ -n "$FAKE_LIST_ERR" ]; then echo "$FAKE_LIST_ERR" >&2; exit 1; fi
	printf '%b' "${FAKE_PROC_LIST:-\$0 \n}"; exit 0 ;;
-C)
	touch "$dir/attached"
	echo x >> "$dir/attaches"
	if [ "$(wc -l < "$dir/attaches")" -le "${FAKE_FAIL_ATTACHES:-0}" ]; then exit 1; fi ;;
*) exit 1 ;;
esac
echo "%begin 1 1 0"
echo "%end 1 1 0"
echo "%pause %4"
n=1
while IFS= read -r line; do
	n=$((n+1))
	echo "$line" >> "$dir/commands"
	case "$line" in
	"capture-pane -p -t %8") exit 0 ;;
	esac
	echo "%begin 2 $n 1"
	case "$line" in
	display-message*)
		echo "$FAKE_FLAGS|4242|1700000000|$FAKE_VERSION"
		if [ -n "$FAKE_STALL" ]; then echo "%end 2 $n 1"; exec sleep 600; fi ;;
	"refresh-client -B 'bad:"*) echo "%error 2 $n 1"; continue ;;
	"capture-pane -p -t %9") echo "%end 2 $n 1"; n=$((n+1)); echo "%begin 2 $n 1" ;;
	"capture-pane -p -t %7")
		echo "%end 2 $n 1"
		for i in 1 2 3 4 5; do echo "%sessions-changed"; done
		continue ;;
	list-sessions*)
		if [ -n "$FAKE_LIST_SLEEP" ]; then sleep "$FAKE_LIST_SLEEP"; fi
		printf '%b' "${FAKE_CTL_LIST:-\$0 \n}" ;;
	switch-client*)
		if [ -n "$FAKE_SWITCH_ERR" ]; then echo "%error 2 $n 1"; continue; fi
		echo "%end 2 $n 1"
		echo "%session-changed \$1 dev"
		continue ;;
	capture-pane*) echo '%pause %3'; echo '%continue %4'; echo 'plain text' ;;
	"refresh-client -A '%3:continue'") echo '%continue %4' ;;
	"refresh-client -A '%4:continue'") echo '%continue %4' ;;
	esac
	echo "%end 2 $n 1"
done
`

func writeFake(t *testing.T, version, flags string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tmux")
	if err := os.WriteFile(p, []byte(fakeTmux), 0o700); err != nil { //nolint:gosec // G306: the fake must be executable
		t.Fatal(err)
	}
	t.Setenv("FAKE_VERSION", version)
	t.Setenv("FAKE_FLAGS", flags)
	return p
}

const goodFlags = "attached,focused,control-mode,ignore-size,pause-after=1,UTF-8"

func TestOlderThan32IsRefusedBeforeAttach(t *testing.T) {
	prog := writeFake(t, "3.1c", goodFlags)
	r := startClientFor(t, prog, filepath.Join(filepath.Dir(prog), "s"), nil)
	select {
	case err := <-r.done:
		r.done <- err // for the cleanup
		if !errors.Is(err, tmux.ErrTooOld) {
			t.Fatalf("Run = %v, want ErrTooOld", err)
		}
	case <-time.After(wait):
		t.Fatal("Run did not stop on tmux 3.1c")
	}
	if r.State() != tmux.StateTooOld {
		t.Fatalf("state %s, want too_old", r.State())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(prog), "attached")); err == nil {
		t.Fatal("the client attached to tmux 3.1c")
	}
	if ev := findEvent(t, r.logs, "tmux.exit"); ev["reason"] != "too_old" {
		t.Fatalf("tmux.exit = %v, want reason too_old", ev)
	}
}

// A tmux that accepts the flags but does not list them in client_flags (as
// 3.1c's -F did) is refused: the gate reads client_flags, never -F.
func TestATmuxThatLacksTheFlagsIsRefused(t *testing.T) {
	for _, flags := range []string{"attached,control-mode,UTF-8", "attached,control-mode,ignore-size", "attached,control-mode,pause-after=1", ""} {
		t.Run(flags, func(t *testing.T) {
			prog := writeFake(t, "3.2a", flags)
			r := startClientFor(t, prog, filepath.Join(filepath.Dir(prog), "s"), nil)
			select {
			case err := <-r.done:
				r.done <- err
				if !errors.Is(err, tmux.ErrTooOld) {
					t.Fatalf("Run = %v, want ErrTooOld", err)
				}
			case <-time.After(wait):
				t.Fatal("Run did not stop on a tmux without the flags")
			}
			if r.Up() {
				t.Fatal("Up with the flags missing")
			}
		})
	}
}

// %pause is honoured from any reply; %continue only from the reply to the
// client's own refresh-client -A for that pane (01 E2, the 7c1 rule).
func TestPauseFromAnyReplyContinueOnlyFromOwnRequest(t *testing.T) {
	prog := writeFake(t, "3.2a", goodFlags)
	r := startClientFor(t, prog, filepath.Join(filepath.Dir(prog), "s"), nil)
	r.waitState(t, tmux.StateUp)
	ctx := ctxT(t)
	waitPaused(t, r, 4, true) // the %pause notification

	if _, err := r.Capture(ctx, 3); err != nil { // its reply holds %pause %3 and %continue %4
		t.Fatal(err)
	}
	if !r.Paused(3) {
		t.Fatal("a pause line inside a capture reply was not honoured")
	}
	if !r.Paused(4) {
		t.Fatal("a continue line inside a capture reply resumed a pane")
	}
	if err := r.Continue(ctx, 3); err != nil { // its reply holds %continue %4 only
		t.Fatal(err)
	}
	if !r.Paused(3) || !r.Paused(4) {
		t.Fatalf("after continuing %%3 with a reply for %%4: paused(3)=%v paused(4)=%v, want both", r.Paused(3), r.Paused(4))
	}
	if err := r.Continue(ctx, 4); err != nil {
		t.Fatal(err)
	}
	if r.Paused(4) {
		t.Fatal("the reply to our own refresh-client -A for pane 4 did not resume it")
	}
	cmds, err := os.ReadFile(filepath.Join(filepath.Dir(prog), "commands"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("commands sent:\n%s", cmds)
}

func waitPaused(t *testing.T, r *running, pane tmux.PaneID, want bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	for r.Paused(pane) != want {
		select {
		case <-ctx.Done():
			t.Fatalf("pane %s paused never became %v", pane, want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// A reply from this client with no command waiting means the stream lost
// its place: the client drops the attach (tmux.exit desync) and attaches
// again.
func TestAReplyForNoCommandIsADesync(t *testing.T) {
	prog := writeFake(t, "3.2a", goodFlags)
	r := startClientFor(t, prog, filepath.Join(filepath.Dir(prog), "s"), nil)
	r.waitState(t, tmux.StateUp)
	if _, err := r.Capture(ctxT(t), 9); err != nil { // answered, then one reply too many
		t.Fatalf("Capture: %v", err)
	}
	waitEvent(t, r.logs, "tmux.exit", map[string]any{"reason": "desync"})
	waitEventCount(t, r.logs, "tmux.attached", 2)
}
