package tmux_test

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

func TestNotifications(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		// %output: tmux writes bytes below space and the backslash as \ooo
		// and every other byte (UTF-8 included) as it is.
		{
			"output with escapes and UTF-8", `%output %1 tab\011here back\134slash \033[1mbold\033[0m café\015\012`,
			[]string{`output %1 "tab\there back\\slash \x1b[1mbold\x1b[0m café\r\n"`},
		},
		{"output of every escaped byte", `%output %7 \000\001\037\134\177`, []string{`output %7 "\x00\x01\x1f\\\x7f"`}},
		{"output with an empty value", `%output %2 `, []string{`output %2 ""`}},
		{"output keeps spaces", `%output %2   two  spaces `, []string{`output %2 "  two  spaces "`}},
		{"output with a short escape", `%output %1 ab\01`, []string{`discarded bad_escape 16`}},
		{"output with a non-octal escape", `%output %1 a\08b`, []string{`discarded bad_escape 16`}},
		{"output with an escape past one byte", `%output %1 \400`, []string{`discarded bad_escape 15`}},
		{"output with a lone backslash", `%output %1 \`, []string{`discarded bad_escape 12`}},
		{"output with no value", `%output %1`, []string{`discarded bad_notification 10`}},
		{"output with a bad pane id", `%output @1 x`, []string{`discarded bad_notification 12`}},
		{"output with a pane id too large", `%output %99999999999 x`, []string{`discarded bad_notification 22`}},
		{"output with a signed pane id", `%output %-1 x`, []string{`discarded bad_notification 13`}},

		// %extended-output: pane, age in ms, arguments for future use, a
		// lone ":", then the value.
		{"extended output", `%extended-output %0 0 : extended 2`, []string{`extended-output %0 age=0s "extended 2"`}},
		{
			"extended output with an age and future arguments", `%extended-output %4 1500 f1 f2 : a : b\012`,
			[]string{`extended-output %4 age=1.5s "a : b\n"`},
		},
		{"extended output with an empty value", `%extended-output %4 3 : `, []string{`extended-output %4 age=3ms ""`}},
		{"extended output without the colon", `%extended-output %4 3 data`, []string{`discarded bad_notification 26`}},
		{"extended output with a bad age", `%extended-output %4 x : d`, []string{`discarded bad_notification 25`}},
		{"extended output with a bad escape", `%extended-output %4 1 : \9`, []string{`discarded bad_escape 26`}},

		{"pause", `%pause %12`, []string{`pause %12`}},
		{"continue", `%continue %0`, []string{`continue %0`}},
		{"pause without a pane", `%pause`, []string{`discarded bad_notification 6`}},
		{"continue with an extra word", `%continue %0 x`, []string{`discarded bad_notification 14`}},

		{"exit", `%exit`, []string{`exit ""`}},
		{"exit with a reason", `%exit detached (from session work)`, []string{`exit "detached (from session work)"`}},

		// %subscription-changed: name $s @w index %p ... : value, with "-"
		// for what the subscription does not cover. The value is kept byte
		// for byte, including a " : " inside it and a trailing space.
		{
			"session subscription", `%subscription-changed fleet-input $0 - - - : $0=1791642745 $1=1791642700 `,
			[]string{`subscription fleet-input $0 - - - "$0=1791642745 $1=1791642700 "`},
		},
		{
			"pane subscription with a colon in the value", `%subscription-changed pane-size $0 @1 1 %1 : %1 : 80 x 24`,
			[]string{`subscription pane-size $0 @1 1 %1 "%1 : 80 x 24"`},
		},
		{"subscription with future arguments", `%subscription-changed s $3 @4 5 %6 f1 f2 : v`, []string{`subscription s $3 @4 5 %6 "v"`}},
		{
			"a name this client never subscribed is still read", `%subscription-changed never-asked $0 - - - : x`,
			[]string{`subscription never-asked $0 - - - "x"`},
		},
		{"subscription with an empty value", `%subscription-changed s $0 - - - : `, []string{`subscription s $0 - - - ""`}},
		{
			"subscription name with a space (names must have none)", `%subscription-changed pane size $0 @0 0 %0 : v`,
			[]string{`discarded bad_notification 46`},
		},
		{"subscription without the colon", `%subscription-changed s $0 - - - v`, []string{`discarded bad_notification 34`}},
		{"subscription with too few fields", `%subscription-changed s $0 : v`, []string{`discarded bad_notification 30`}},
		{"subscription with a bad session id", `%subscription-changed s 0 - - - : v`, []string{`discarded bad_notification 35`}},

		// Window and session changes (the row-7 ruling's set). Names are
		// the developer's text, kept byte for byte.
		{"window added", `%window-add @1`, []string{`window-add @1`}},
		{"window added to another session", `%unlinked-window-add @12`, []string{`unlinked-window-add @12`}},
		{"window in another session closed", `%unlinked-window-close @3`, []string{`unlinked-window-close @3`}},
		{"window renamed", `%window-renamed @0 my  win `, []string{`window-renamed @0 "my  win "`}},
		{"window renamed to nothing", `%window-renamed @0 `, []string{`window-renamed @0 ""`}},
		{"session changed", `%session-changed $1 other`, []string{`session-changed $1 "other"`}},
		{"session changed to a name with spaces", `%session-changed $0 a b : c`, []string{`session-changed $0 "a b : c"`}},
		{"window added with a pane id", `%window-add %1`, []string{`discarded bad_notification 14`}},
		{"window added with no id", `%window-add`, []string{`discarded bad_notification 11`}},
		{"window added with an extra word", `%unlinked-window-add @1 x`, []string{`discarded bad_notification 25`}},
		{"window closed with a bad id", `%unlinked-window-close @x`, []string{`discarded bad_notification 25`}},
		{"window renamed with no name", `%window-renamed @0`, []string{`discarded bad_notification 18`}},
		{"window renamed with a session id", `%window-renamed $0 x`, []string{`discarded bad_notification 20`}},
		{"session changed with no name", `%session-changed $1`, []string{`discarded bad_notification 19`}},
		{"session changed with a window id", `%session-changed @1 x`, []string{`discarded bad_notification 21`}},
		// A nested loop's value: spaces and repeated "=" kept as they are.
		{
			"nested window-activity loop", `%subscription-changed win-act $0 - - - : @0=1791643800 @2=1791643802 @3=1791643803 `,
			[]string{`subscription win-act $0 - - - "@0=1791643800 @2=1791643802 @3=1791643803 "`},
		},

		// Everything else is kept by name only.
		{"a notification this parser does not read", `%unlinked-window-renamed @1 my secret window`, []string{`unknown unlinked-window-renamed`}},
		{"a closed linked window is not read yet", `%window-close @1`, []string{`unknown window-close`}},
		{"a notification with no arguments", `%sessions-changed`, []string{`unknown sessions-changed`}},
		{"a bare percent sign", `%`, []string{`discarded bad_notification 1`}},
		{"a list-panes line outside a reply", `%0 $0 @0`, []string{`discarded bad_notification 8`}},
		{"a name with characters tmux never uses", `%Window-Add @1`, []string{`discarded bad_notification 14`}},
		{"a name that is too long", "%" + strings.Repeat("a", 65), []string{`discarded bad_notification 66`}},
		{"a name of the longest length kept", "%" + strings.Repeat("a", 64) + " x", []string{"unknown " + strings.Repeat("a", 64)}},
		{"two spaces after the name", `%pause  %1`, []string{`discarded bad_notification 10`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkMessages(t, collect(t, strings.NewReader(tc.in+"\n"), tmux.Options{}), tc.want)
		})
	}
}

func TestNotificationsInsideAReplyStayInItsBody(t *testing.T) {
	t.Parallel()
	// tmux writes %pause and %continue inside the reply to the client's own
	// "refresh-client -A" (recorded on 3.2a and 3.7c), but a capture of a
	// pane can print the same lines. Only the client knows which command a
	// reply answers, so the parser keeps every line in the body.
	in := "%begin 1 2 1\n%output %0 x\n%subscription-changed s $0 - - - : v\n%exit\n%window-add @1\n" +
		"%pause %1\n%continue %2\n%pause\n%continue %3 x\n%end 1 2 1\n"
	checkMessages(t, collect(t, strings.NewReader(in), tmux.Options{}), []string{
		`reply ok ["%output %0 x" "%subscription-changed s $0 - - - : v" "%exit" "%window-add @1" "%pause %1" "%continue %2" "%pause" "%continue %3 x"] dropped=0`,
	})
}

func TestControlNotesAreTheWellFormedPauseAndContinueLinesOfAReply(t *testing.T) {
	t.Parallel()
	// A capture of a pane that printed "%continue %3" and "%pause %4": the
	// body is intact, and ControlNotes finds both; the client honours them
	// only for its own refresh-client -A reply.
	in := "%begin 9 40 1\nprompt$ cat notes\n%continue %3\n%pause %4\n%pause %4 x\n%continue\n %pause %5\nend\n%end 9 40 1\n"
	p := tmux.NewParser(strings.NewReader(in), tmux.Options{})
	m, err := p.Next()
	reply, ok := m.(tmux.Reply)
	if err != nil || !ok {
		t.Fatalf("Next = %T, %v", m, err)
	}
	want := []string{"prompt$ cat notes", "%continue %3", "%pause %4", "%pause %4 x", "%continue", " %pause %5", "end"}
	if strings.Join(reply.Body, "|") != strings.Join(want, "|") || reply.Dropped != 0 {
		t.Errorf("body = %q, dropped %d; want %q", reply.Body, reply.Dropped, want)
	}
	var notes []string
	for _, n := range reply.ControlNotes() {
		notes = append(notes, render(n))
	}
	checkMessages(t, notes, []string{`continue %3`, `pause %4`})
	if got := (tmux.Reply{Body: []string{"x", "%end 1 2 1"}}).ControlNotes(); len(got) != 0 {
		t.Errorf("ControlNotes of a reply with none = %v", got)
	}
}
