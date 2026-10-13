package tmux_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// The client read is E13's on-demand primitive (02h): one list-clients row
// per attached client. The table pins the parse: a person, the agent's own
// control-mode client, and every malformed shape the read refuses whole.
func TestParseClients(t *testing.T) {
	t.Parallel()
	epoch := func(s int64) time.Time { return time.Unix(s, 0) }
	cases := []struct {
		name string
		body []string
		want []tmux.ClientInfo
		fail bool
	}{
		{
			"no clients is no input",
			nil,
			[]tmux.ClientInfo{},
			false,
		},
		{
			"a person at a pane",
			[]string{"54127 1791889584 0 $0 %0"},
			[]tmux.ClientInfo{{PID: 54127, Activity: epoch(1791889584), Control: false, Session: 0, Pane: 0}},
			false,
		},
		{
			"the control client is marked",
			[]string{"54128 1791889584 1 $0 %0"},
			[]tmux.ClientInfo{{PID: 54128, Activity: epoch(1791889584), Control: true, Session: 0, Pane: 0}},
			false,
		},
		{
			"two clients keep their rows",
			[]string{"54127 1791889584 0 $1 %3", "54128 1791889590 0 $0 %0"},
			[]tmux.ClientInfo{
				{PID: 54127, Activity: epoch(1791889584), Session: 1, Pane: 3},
				{PID: 54128, Activity: epoch(1791889590), Session: 0, Pane: 0},
			},
			false,
		},
		{name: "a short row fails the read", body: []string{"54127 1791889584 0 $0"}, fail: true},
		{name: "a long row fails the read", body: []string{"54127 1791889584 0 $0 %0 x"}, fail: true},
		{name: "a bad pid fails the read", body: []string{"x 1791889584 0 $0 %0"}, fail: true},
		{name: "a bad key time fails the read", body: []string{"54127 yesterday 0 $0 %0"}, fail: true},
		{name: "a bad control flag fails the read", body: []string{"54127 1791889584 2 $0 %0"}, fail: true},
		{name: "a named session fails the read", body: []string{"54127 1791889584 0 work %0"}, fail: true},
		{name: "a window id fails the read", body: []string{"54127 1791889584 0 $0 @0"}, fail: true},
		{name: "one bad row fails the read", body: []string{"54127 1791889584 0 $0 %0", "nope"}, fail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tmux.ParseClientsForTest(tc.body)
			if tc.fail {
				if err == nil {
					t.Fatalf("ParseClients(%q) = %+v, want an error", tc.body, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseClients(%q): %v", tc.body, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParseClients(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}
