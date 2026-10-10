package tmux_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// One pick rule for every server (row-7 arbiter ruling on 7c2b): the lowest
// session not marked internal; if none, the lowest internal one; no session
// at all is absent. The input is `list-sessions -F '#{session_id}
// #{@fleet_internal}'`.
func TestPickSession(t *testing.T) {
	for _, tc := range []struct {
		name         string
		list         string
		want         tmux.SessionID
		wantInternal bool
		wantAbsent   bool
	}{
		{"none", "", 0, false, true},
		{"internal only: the lowest internal", "$4 attach_view\n$2 attach_view\n", 2, true, false},
		{"an internal id below a developer id: the developer one", "$0 attach_view\n$3 \n$5 \n", 3, false, false},
		{"developer only", "$7 \n$1 \n", 1, false, false},
		{"any mark counts as internal", "$0 x\n$1 \n", 1, false, false},
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
	if _, _, err := tmux.PickSessionForTest("@1 x\n"); err == nil || tmux.IsAbsentForTest(err) {
		t.Fatalf("a malformed line = %v, want an error that is not absence", err)
	}
}
