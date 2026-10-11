package tmux_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// One pick rule for every server (row-7 arbiter ruling on 7c2b): the lowest
// session not marked internal; if none, the lowest internal one; no session
// at all is absent. The input is `list-sessions -F '#{session_id}
// #{==:#{@fleet_internal},attach_view}'`: 1 only for the exact mark.
func TestPickSession(t *testing.T) {
	for _, tc := range []struct {
		name         string
		list         string
		want         tmux.SessionID
		wantInternal bool
		wantAbsent   bool
	}{
		{"none", "", 0, false, true},
		{"internal only: the lowest internal", "$4 1\n$2 1\n", 2, true, false},
		{"an internal id below a developer id: the developer one", "$0 1\n$3 0\n$5 0\n", 3, false, false},
		{"developer only", "$7 0\n$1 0\n", 1, false, false},
		{"only the exact mark is internal (0 is a developer session)", "$0 0\n$1 1\n", 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, internal, err := tmux.PickSessionForTest(tc.list)
			if tc.wantAbsent {
				if !tmux.IsAbsentForTest(err) {
					t.Fatalf("pick = %v, %v, %v; want absent", got, internal, err)
				}
				return
			}
			if err != nil || got != tc.want || internal != tc.wantInternal {
				t.Fatalf("pick = %v, %v, %v; want %v, %v", got, internal, err, tc.want, tc.wantInternal)
			}
		})
	}
	// Anything but "$N 0" or "$N 1" is not the asked shape. A mark with a
	// newline once put a bare line here and failed every attach.
	for _, bad := range []string{"@1 1\n", "$0 x\n", "$0 \n", "$0\n", "$0 attach_view\n", "$0 0\nz\n", "$0 10\n"} {
		if _, _, err := tmux.PickSessionForTest(bad); err == nil || tmux.IsAbsentForTest(err) {
			t.Fatalf("list %q = %v, want an error that is not absence", bad, err)
		}
	}
}
