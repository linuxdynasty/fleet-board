package goalfile_test

import (
	"strings"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

func TestParse(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		data     string
		state    typev1.GoalLineState
		first    string
		consent  string
		watchPID uint32
		owner    string
		action   string
	}{
		{
			name:    "working objective",
			data:    "Work issue acme/repo#12 on branch fleet/12: fix the leak.\nCONSENT: push,pr-open,merge\n",
			state:   typev1.GoalLineState_GOAL_LINE_STATE_WORKING,
			first:   "Work issue acme/repo#12 on branch fleet/12: fix the leak.",
			consent: "CONSENT: push,pr-open,merge",
		},
		{
			name:  "done",
			data:  "DONE\n",
			state: typev1.GoalLineState_GOAL_LINE_STATE_DONE,
			first: "DONE",
		},
		{
			name:  "done without trailing newline",
			data:  "DONE",
			state: typev1.GoalLineState_GOAL_LINE_STATE_DONE,
			first: "DONE",
		},
		{
			name:     "blocked watch",
			data:     "BLOCKED: waiting on CI WATCH=4242\n",
			state:    typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH,
			first:    "BLOCKED: waiting on CI WATCH=4242",
			watchPID: 4242,
		},
		{
			name:   "blocked owner action",
			data:   "BLOCKED: review loop OWNER=fleet ACTION=decide\n",
			state:  typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_OWNER,
			first:  "BLOCKED: review loop OWNER=fleet ACTION=decide",
			owner:  "fleet",
			action: "decide",
		},
		{
			name:   "owner and action cut at 256 bytes",
			data:   "BLOCKED: x OWNER=" + strings.Repeat("o", 300) + " ACTION=" + strings.Repeat("a", 300) + "\n",
			state:  typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_OWNER,
			first:  "BLOCKED: x OWNER=" + strings.Repeat("o", 300) + " ACTION=" + strings.Repeat("a", 300),
			owner:  strings.Repeat("o", 256),
			action: strings.Repeat("a", 256),
		},
		{
			name:  "blocked bare is stuck",
			data:  "BLOCKED: something odd\n",
			state: typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_STUCK,
			first: "BLOCKED: something odd",
		},
		{
			name:  "blocked owner without action is stuck",
			data:  "BLOCKED: waiting OWNER=ann\n",
			state: typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_STUCK,
			first: "BLOCKED: waiting OWNER=ann",
			owner: "ann",
		},
		{
			name:  "blocked bad watch pid is stuck",
			data:  "BLOCKED: waiting WATCH=nope\n",
			state: typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_STUCK,
			first: "BLOCKED: waiting WATCH=nope",
		},
		{
			name:  "empty file works",
			data:  "",
			state: typev1.GoalLineState_GOAL_LINE_STATE_WORKING,
			first: "",
		},
		{
			name:    "consent is the first consent line only",
			data:    "Working on it\nnote: CONSENT: not this one\nCONSENT: push\n",
			state:   typev1.GoalLineState_GOAL_LINE_STATE_WORKING,
			first:   "Working on it",
			consent: "CONSENT: push",
		},
		{
			name:  "control characters stripped",
			data:  "Work\x00 on\x1bit \u007fand\x9f it\n",
			state: typev1.GoalLineState_GOAL_LINE_STATE_WORKING,
			first: "Work onit and it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := goalfile.Parse([]byte(tc.data))
			if got.State != tc.state {
				t.Fatalf("state = %v, want %v (first %q)", got.State, tc.state, got.FirstLine)
			}
			if got.FirstLine != tc.first {
				t.Fatalf("first = %q, want %q", got.FirstLine, tc.first)
			}
			if got.ConsentLine != tc.consent {
				t.Fatalf("consent = %q, want %q", got.ConsentLine, tc.consent)
			}
			if got.WatchPID != tc.watchPID {
				t.Fatalf("watch pid = %d, want %d", got.WatchPID, tc.watchPID)
			}
			if got.Owner != tc.owner {
				t.Fatalf("owner = %q, want %q", got.Owner, tc.owner)
			}
			if got.Action != tc.action {
				t.Fatalf("action = %q, want %q", got.Action, tc.action)
			}
			if got.Typed {
				t.Fatalf("Typed = true; want false (no trailer in the table cases)")
			}
		})
	}
}

func TestParseTypedMarker(t *testing.T) {
	t.Parallel()
	if got := goalfile.Parse([]byte("migrate the parser\nCONSENT: push\nTYPED: yes\n")); !got.Typed {
		t.Fatalf("Typed = false; want true with the trailer present")
	}
	if got := goalfile.Parse([]byte("migrate the parser\nCONSENT: push\n")); got.Typed {
		t.Fatalf("Typed = true; want false without the trailer")
	}
	if got := goalfile.Parse([]byte("DONE\nCONSENT: push\nTYPED: yes\n")); !got.Typed ||
		got.State != typev1.GoalLineState_GOAL_LINE_STATE_DONE {
		t.Fatalf("Typed = %v state = %v; want true DONE", got.Typed, got.State)
	}
}

func TestParseCapsFirstLine(t *testing.T) {
	t.Parallel()
	big := make([]byte, 0, 5000)
	big = append(big, "Work "...)
	for len(big) < 5000 {
		big = append(big, 'x')
	}
	got := goalfile.Parse(big)
	if len(got.FirstLine) > 4096 {
		t.Fatalf("first line is %d bytes, over the 4 KiB cap", len(got.FirstLine))
	}
}
