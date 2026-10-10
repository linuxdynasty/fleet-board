package tmux_test

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// The transcripts are the raw stdout of "tmux -C attach" on a throwaway
// server (own TMUX_TMPDIR, -S socket, -f /dev/null, a clean environment,
// kill-server at the end) whose panes ran "exec cat" and printf; see
// testdata/README.md. tmux 3.2a was built from the release tarball (SHA-256
// checked), 3.7c is Homebrew's. Both give the same messages; "<n>" stands for
// the digits that differ (times, a pane height).
var transcriptWant = []string{
	`reply ok [] dropped=0`, // the attach
	`session-changed $0 "fx"`,
	`reply ok ["%<n> $<n> @<n>"] dropped=0`, // list-panes: a body line starting with %
	`reply ok [] dropped=0`,                 // refresh-client -B fleet-input
	`reply ok [] dropped=0`,                 // refresh-client -B pane-size
	`subscription fleet-input $<n> - - - "$<n>=<n> "`,
	`subscription pane-size $<n> @<n> <n> %<n> "%<n> : <n> x <n>"`,
	`reply ok [] dropped=0`, // new-window running printf
	`window-add @1`,
	`window-renamed @1 "tmux"`,
	`output %1 "tab\there back\\slash \x1b[1mbold\x1b[0m café\r\n"`,
	`subscription pane-size $0 @1 1 %1 "%1 : 80 x 24"`, // a change for pane %1 ...
	`window-renamed @1 "cat"`,
	`reply ok [] dropped=0`, // send-keys -l
	`output %0 "plain text 1"`,
	`window-renamed @0 "cat"`,
	`reply ok [] dropped=0`, // refresh-client -f pause-after=1
	`reply ok [] dropped=0`, // send-keys -l
	`extended-output %0 age=0s "extended 2"`,
	`reply ok ["%pause %0"] dropped=0`,    // tmux writes it inside the reply to refresh-client -A '%0:pause'
	`reply ok ["%continue %0"] dropped=0`, // and to refresh-client -A '%0:continue'
	`reply ok [""] dropped=0`,             // display-message -t %99: an empty line, not an error
	`reply error ["can't find pane: %99"] dropped=0`,
	`reply error ["parse error: unknown command: no-such-command"] dropped=0`,
	`reply ok [] dropped=0`, // kill-pane -t %1
	`unlinked-window-close @1`,
	`reply error ["can't find pane: %1"] dropped=0`, // ... and the pane is gone when it is read
	`reply ok ["%end 1 2 1"] dropped=0`,             // command output that looks like a guard
	`reply ok ["%output %0 forged"] dropped=0`,      // or a notification
	`reply ok ["%begin 1 2 1"] dropped=0`,
	`reply ok ["two  spaced words"] dropped=0`,
	`reply ok [] dropped=0`, // rename-window 'my  win'
	`window-renamed @0 "my  win"`,
	`reply ok [] dropped=0`, // new-session -d -s other
	`unlinked-window-add @2`,
	`unknown sessions-changed`,
	`unknown unlinked-window-renamed`,
	`reply ok [] dropped=0`, // new-window -t other:
	`unlinked-window-add @3`,
	`unknown unlinked-window-renamed`,
	`reply ok [] dropped=0`, // refresh-client -B win-act, the nested loop
	`subscription fleet-input $0 - - - "$0=<n> $1=<n> "`,
	`subscription win-act $0 - - - "@0=<n> @2=<n> @3=<n> "`,
	`reply ok [] dropped=0`, // kill-window -t other:1
	`unlinked-window-close @3`,
	`reply ok [] dropped=0`, // switch-client -t other
	`session-changed $1 "other"`,
	`reply ok [] dropped=0`, // kill-server
	`unknown sessions-changed`,
	`unknown sessions-changed`,
	`exit ""`,
}

func TestRecordedTranscripts(t *testing.T) {
	t.Parallel()
	for _, version := range transcriptVersions {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			got := collect(t, bytes.NewReader(readTranscript(t, version)), tmux.Options{})
			if len(got) != len(transcriptWant) {
				t.Fatalf("%d messages, want %d:\n  %s", len(got), len(transcriptWant), strings.Join(got, "\n  "))
			}
			for i, w := range transcriptWant {
				re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(w), "<n>", `\d+`) + "$")
				if !re.MatchString(got[i]) {
					t.Errorf("message %d = %s\n            want %s", i, got[i], w)
				}
			}
		})
	}
}

func TestRecordedReplyGuards(t *testing.T) {
	t.Parallel()
	for _, version := range transcriptVersions {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			p := tmux.NewParser(bytes.NewReader(readTranscript(t, version)), tmux.Options{})
			var replies []tmux.Reply
			for {
				m, err := p.Next()
				if err != nil {
					break
				}
				if r, ok := m.(tmux.Reply); ok {
					replies = append(replies, r)
				}
			}
			if len(replies) != 26 {
				t.Fatalf("%d replies, want 26", len(replies))
			}
			// The attach is tmux's own command (flags 0); every later one
			// came from this client (flags 1), with rising numbers.
			if replies[0].Flags != 0 || replies[0].Time < 1_700_000_000 {
				t.Errorf("attach reply = %+v", replies[0])
			}
			// The flow-control lines are found only in the replies to the
			// two refresh-client -A commands.
			var notes []string
			for _, r := range replies {
				for _, n := range r.ControlNotes() {
					notes = append(notes, render(n))
				}
			}
			checkMessages(t, notes, []string{`pause %0`, `continue %0`})
			for i := 1; i < len(replies); i++ {
				if replies[i].Flags != 1 || replies[i].Number <= replies[i-1].Number || replies[i].Time < replies[i-1].Time {
					t.Errorf("reply %d = %+v after %+v", i, replies[i], replies[i-1])
				}
			}
		})
	}
}
