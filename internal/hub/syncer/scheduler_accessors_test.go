package syncer

import (
	"testing"
	"time"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The scheduler pass waits for the first sync lap and recomputes trust
// at plan time (03c rules 1 and 4).
func TestSchedulerAccessors(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	a := &fakeAdapter{
		caps:     testCaps(),
		work:     func(string, time.Time, *integrationv1.Validators, bool) (*WorkItemPage, error) { return nil, nil },
		claims:   emptyClaims(),
		changes:  noChanges(),
		evidence: noEvidence(),
	}
	sy, _ := testSyncer(s, a)

	if got := sy.RoundsCompleted("github-1"); got != 0 {
		t.Fatalf("RoundsCompleted before any round = %d, want 0", got)
	}

	item := syncItem("7", "alice", "1001", "agent-ready")
	item.Labels[0].AppliedBy = trustPerson("alice", "1001")
	item.Labels[0].AppliedTime = timestamppb.New(syncNow.Add(-2 * time.Hour))
	in := sy.GateInputFor(item, "github-1", "acme/web", syncNow)
	if in.Integration != "github-1" {
		t.Errorf("GateInputFor integration = %q, want github-1", in.Integration)
	}
	if !in.LabelsWithActor || !in.EditEvidence {
		t.Errorf("GateInputFor caps = (%v, %v), want the adapter's (true, true)",
			in.LabelsWithActor, in.EditEvidence)
	}
	if got := Evaluate(in); !got.GetTrusted() {
		t.Errorf("recomputed verdict trusted = false, want true (denial %v)", got.GetDenial())
	}

	edited := syncItem("7", "alice", "1001", "agent-ready")
	edited.Labels[0].AppliedBy = trustPerson("alice", "1001")
	edited.Labels[0].AppliedTime = timestamppb.New(syncNow.Add(-2 * time.Hour))
	edited.LastEditor = trustPerson("mallory", "1003")
	edited.LastEditTime = timestamppb.New(syncNow)
	inEdit := sy.GateInputFor(edited, "github-1", "acme/web", syncNow)
	if got := Evaluate(inEdit); got.GetTrusted() {
		t.Error("recomputed verdict after outsider edit trusted = true, want false (plan-time demotion)")
	}
}
