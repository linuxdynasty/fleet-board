package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// claimStages are the mirror stages the claim loop re-checks (03m: the
// queued and running items).
var claimStages = []int32{
	int32(hubv1.WorkStage_WORK_STAGE_READY),
	int32(hubv1.WorkStage_WORK_STAGE_CLAIMED_ELSEWHERE),
	int32(hubv1.WorkStage_WORK_STAGE_DISPATCHED),
	int32(hubv1.WorkStage_WORK_STAGE_RUNNING),
	int32(hubv1.WorkStage_WORK_STAGE_DONE_CLAIMED),
}

// claimsRound re-checks every candidate item's marker comments (03m: every
// 5 min) and re-stores the stage the claims imply.
func (s *Syncer) claimsRound(ctx context.Context, a Adapter, id string, now time.Time) error {
	ctx, span := s.tracer().Start(ctx, "hub.sync.claims",
		trace.WithAttributes(attribute.String("integration", id)))
	defer span.End()
	candidates, err := statedb.ListClaimCandidates(ctx, s.Store, id, claimStages)
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range candidates {
		if err := s.claimItem(ctx, a, id, c, now); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Syncer) claimItem(ctx context.Context, a Adapter, id string, c statedb.ClaimCandidate, now time.Time) error {
	start := s.now()
	cursor, err := statedb.GetCursor(ctx, s.Store, id, ResourceClaims, c.ItemKey)
	if err != nil {
		return err
	}
	page, err := a.ListClaims(ctx, c.Project, c.ItemKey, validatorsFor(cursor.ETag, 0, false))
	if err != nil {
		if providerErrorOf(err).GetKind() == integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_NOT_FOUND {
			// The item is gone: the mirror row closes (03j); a goal
			// under it is the provider-side rules' business (row 22+).
			key := statedb.SyncKey(id, typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE.String(), c.Project, c.ItemKey)
			stored, getErr := statedb.GetWorkItemState(ctx, s.Store, key)
			if getErr != nil {
				return getErr
			}
			pctx := context.WithoutCancel(ctx) // the publish hook runs on the writer after the waiter may have left
			_, err := statedb.UpdateWorkItemClaims(ctx, s.Store, key, []byte(stored.ClaimsJSON),
				int32(hubv1.WorkStage_WORK_STAGE_CLOSED),
				int32(hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED), now.UnixMilli(),
				func(committed statedb.WorkItemChange) {
					// The close committed: its mirror change publishes to
					// Watch. This runs on the writer in commit order (#393).
					s.publishWorkItems(pctx, []statedb.WorkItemChange{committed})
				})
			if err != nil {
				return err
			}
			return nil
		}
		return s.fail(ctx, id, c.Project, ResourceClaims, err, now)
	}
	s.mergeLimits(id, page.Meta)
	if page.NotModified {
		s.storeLimits(ctx, id, ResourceClaims, c.ItemKey, cursor, page.Meta, now)
		return nil
	}
	claimsJSON, err := marshalClaims(page.Claims)
	if err != nil {
		return err
	}
	key := statedb.SyncKey(id, typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE.String(), c.Project, c.ItemKey)
	stored, err := statedb.GetWorkItemState(ctx, s.Store, key)
	if err != nil {
		return err
	}
	var item integrationv1.WorkItem
	if err := protojson.Unmarshal([]byte(stored.ItemJSON), &item); err != nil {
		return err
	}
	gate := s.repoGate(c.Project)
	verdict := Evaluate(GateInput{
		Item: &item, Gate: s.Gate,
		RepoLabel: repoGateLabel(gate), RepoUsers: repoGateUsers(gate),
		Integration:     id,
		LabelsWithActor: true,
		EditEvidence:    true,
		Now:             now,
	})
	foreign, ours, theirs := splitClaims(page.Claims)
	stage, reason := InitialStage(item.GetState(), verdict, foreign, gate.Enabled)
	pctx := context.WithoutCancel(ctx) // the publish hook runs on the writer after the waiter may have left
	_, err = statedb.UpdateWorkItemClaims(ctx, s.Store, key, claimsJSON, int32(stage), int32(reason), now.UnixMilli(),
		func(committed statedb.WorkItemChange) {
			// The claim move committed: its mirror change publishes to
			// Watch. This runs on the writer in commit order (#393).
			s.publishWorkItems(pctx, []statedb.WorkItemChange{committed})
		})
	if err != nil {
		return err
	}
	cursorOut := statedb.CursorValue{Resource: ResourceClaims, Page: c.ItemKey, ETag: page.Validators.GetEtag()}
	if _, err := statedb.UpsertWorkItemPage(ctx, s.Store, id, nil,
		[]statedb.CursorValue{cursorOut}, rateLimitValues(page.Meta), now.UnixMilli(), nil); err != nil {
		return err
	}
	if s.Metrics != nil {
		for _, cl := range page.Claims {
			byFleet := "false"
			if cl.GetByFleet() {
				byFleet = "true"
			}
			s.Metrics.claimsSeen.Add(ctx, 1, metricWith("integration", id, "by_this_fleet", byFleet))
		}
		s.Metrics.requests.Add(ctx, 1, metricWith("integration", id, "resource", ResourceClaims, "status", "ok"))
		s.Metrics.poll.Record(ctx, s.now().Sub(start).Seconds(), metricWith("integration", id, "resource", ResourceClaims))
	}
	// A foreign claim with an earlier timestamp than ours races the
	// dispatch: a person decides (03m double_claim).
	if ours != nil && theirs != nil && theirs.GetClaimTime().AsTime().Before(ours.GetClaimTime().AsTime()) {
		s.raise(id, c.Project, "double_claim",
			"foreign claim by "+theirs.GetBy().GetLogin()+" is earlier than ours", now)
	}
	return nil
}

// splitClaims tells a foreign marker from the fleet's own.
func splitClaims(claims []*integrationv1.Claim) (foreign bool, ours, theirs *integrationv1.Claim) {
	for _, cl := range claims {
		if cl.GetByFleet() {
			if ours == nil {
				ours = cl
			}
		} else {
			foreign = true
			if theirs == nil {
				theirs = cl
			}
		}
	}
	return foreign, ours, theirs
}

func marshalClaims(claims []*integrationv1.Claim) ([]byte, error) {
	raw := make([]json.RawMessage, 0, len(claims))
	for _, cl := range claims {
		b, err := protojson.Marshal(cl)
		if err != nil {
			return nil, err
		}
		raw = append(raw, b)
	}
	return json.Marshal(raw)
}
