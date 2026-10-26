package goals_test

import (
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// TestLiveMatchesStore proves the SQL backstop (the partial unique
// indexes of migration 28) holds exactly the LIVE set of 03l: every
// contract phase is live in Go exactly when the store predicate holds
// it. A new live phase needs a migration that extends both predicates,
// and this test names it.
func TestLiveMatchesStore(t *testing.T) {
	t.Parallel()
	for name, num := range hubv1.GoalPhase_value {
		p := hubv1.GoalPhase(num)
		if name == "GOAL_PHASE_UNSPECIFIED" {
			continue
		}
		if goals.Live(p) != statedb.DispatchedLive(name) {
			t.Errorf("phase %s: goals.Live = %v, store = %v", name, goals.Live(p), statedb.DispatchedLive(name))
		}
		if goals.ClassOf(p) == goals.ClassEnded && p != hubv1.GoalPhase_GOAL_PHASE_MERGED &&
			p != hubv1.GoalPhase_GOAL_PHASE_CLOSED &&
			p != hubv1.GoalPhase_GOAL_PHASE_START_FAILED &&
			p != hubv1.GoalPhase_GOAL_PHASE_START_EXHAUSTED &&
			p != hubv1.GoalPhase_GOAL_PHASE_EXPIRED &&
			p != hubv1.GoalPhase_GOAL_PHASE_ABANDONED &&
			p != hubv1.GoalPhase_GOAL_PHASE_CLEARED &&
			p != hubv1.GoalPhase_GOAL_PHASE_CANCELLED {
			t.Errorf("phase %s: ended but not in the ENDED list", name)
		}
	}
}
