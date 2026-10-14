package goalfile_test

import (
	"os"
	"path/filepath"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

// TestFixtures parses every eshu goal-file fixture in testdata: one file
// per first-line shape the tracker reports (row 9 acceptance).
func TestFixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file  string
		state typev1.GoalLineState
	}{
		{"working.goal", typev1.GoalLineState_GOAL_LINE_STATE_WORKING},
		{"blocked-watch.goal", typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_WATCH},
		{"blocked-owner.goal", typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_OWNER},
		{"blocked-stuck.goal", typev1.GoalLineState_GOAL_LINE_STATE_BLOCKED_STUCK},
		{"done.goal", typev1.GoalLineState_GOAL_LINE_STATE_DONE},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			got := goalfile.Parse(data)
			if got.State != tc.state {
				t.Fatalf("state = %v, want %v (first %q)", got.State, tc.state, got.FirstLine)
			}
			if got.FirstLine == "" {
				t.Fatal("first line is empty")
			}
		})
	}
}
