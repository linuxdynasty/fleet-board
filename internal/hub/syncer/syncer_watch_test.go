package syncer

import (
	"context"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Watch publishing: a committed page and claim move publish their mirror
// changes to the sink with the page's stamps. Fakes live in syncer_test.go.

// A committed page and claim move publish their mirror changes to the
// Watch sink with the page's stamps; an identical second round publishes
// nothing.
func TestPollOncePublishesMirrorChanges(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	a := &fakeAdapter{
		caps: testCaps(),
		work: func(string, time.Time, *integrationv1.Validators, bool) (*WorkItemPage, error) {
			return &WorkItemPage{
				Items:      []*integrationv1.WorkItem{syncItem("9", "alice", "1001", "agent-ready")},
				Validators: &integrationv1.Validators{Etag: "e1"},
				NextSince:  &integrationv1.ChangeCursor{UpdatedAfterTime: timestamppb.New(syncNow)},
				Meta:       syncMeta(),
			}, nil
		},
		claims: emptyClaims(), changes: noChanges(), evidence: noEvidence(),
	}
	sy, _ := testSyncer(s, a)
	var wi []statedb.WorkItemChange
	var cr []statedb.ChangeRequestChange
	sy.OnWorkItems = func(_ context.Context, c []statedb.WorkItemChange) { wi = append(wi, c...) }
	sy.OnChangeRequests = func(_ context.Context, c []statedb.ChangeRequestChange) { cr = append(cr, c...) }
	if err := sy.PollOnce(context.Background(), "github-1", false); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(wi) == 0 || !wi[0].Created || wi[0].HubSeq <= 0 || wi[0].Before != nil {
		t.Fatalf("work item changes = %+v, want the page create first", wi)
	}
	n := len(wi)
	if err := sy.PollOnce(context.Background(), "github-1", false); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if len(wi) != n {
		t.Fatalf("second round published %d more changes, want silence", len(wi)-n)
	}
	crPage := func() *ChangeRequestList {
		return &ChangeRequestList{Meta: syncMeta(), ChangeRequests: []*integrationv1.ChangeRequest{{
			Ref: &typev1.WorkItemRef{Key: &typev1.WorkItemKey{
				Integration: &typev1.IntegrationId{Value: "github-1"}, Project: "acme/web", Key: "41",
			}},
			UpdateTime: timestamppb.New(syncNow),
		}}}
	}
	a.changes = func(string, []string, []string, string) (*ChangeRequestList, error) { return crPage(), nil }
	if _, err := sy.VerifyBranches(context.Background(), "github-1", "acme/web", []string{"fleet/9"}, nil, ""); err != nil {
		t.Fatalf("VerifyBranches: %v", err)
	}
	if len(cr) != 1 || !cr[0].Created || cr[0].HubSeq <= 0 {
		t.Fatalf("change request changes = %+v, want one create", cr)
	}
	if _, err := sy.VerifyBranches(context.Background(), "github-1", "acme/web", []string{"fleet/9"}, nil, ""); err != nil {
		t.Fatalf("VerifyBranches: %v", err)
	}
	if len(cr) != 1 {
		t.Fatalf("second verify published %d more changes, want silence", len(cr)-1)
	}
}
