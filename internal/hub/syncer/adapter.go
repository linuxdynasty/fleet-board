package syncer

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

// Adapter is the provider-neutral read surface the hub's syncer polls
// (service.proto IntegrationProviderService, 03j). Each call carries the
// cursor and validators the hub stored; each answer carries the next ones,
// the rate limits and a typed error. Adapters keep no sync state, so one
// loop serves GitHub today and GitLab, Jira or a plugin tomorrow.
type Adapter interface {
	// Describe reports what the adapter supports; the hub checks it first
	// and never asks for what it lacks.
	Describe() *integrationv1.IntegrationCapabilities
	// ListWorkItems pages a project's changed items; evidenceLabels names
	// the trusted labels whose apply the adapter must report.
	ListWorkItems(ctx context.Context, project string, since time.Time, validators *integrationv1.Validators, includeClosed bool) (*WorkItemPage, error)
	// ListClaims returns an item's full marker-comment list for the claim
	// loop (ETagged; withdrawals show as absence).
	ListClaims(ctx context.Context, project, itemKey string, validators *integrationv1.Validators) (*ClaimPage, error)
	// ListChangeRequests finds change requests by head branch, head sha,
	// or the linked work item's key ("" skips that lookup).
	ListChangeRequests(ctx context.Context, project string, branches, commits []string, linkedKey string) (*ChangeRequestList, error)
	// FillEvidence fetches editor and label-apply evidence for the numbered
	// issues, for the requested labels only.
	FillEvidence(ctx context.Context, project string, numbers []int, labels []string) ([]Evidence, *integrationv1.ResponseMeta, error)
}

// WorkItemPage is one policed page of work items.
type WorkItemPage struct {
	Items       []*integrationv1.WorkItem
	Validators  *integrationv1.Validators
	NotModified bool
	NextSince   *integrationv1.ChangeCursor
	Meta        *integrationv1.ResponseMeta
}

// ClaimPage is one item's marker comments.
type ClaimPage struct {
	Claims      []*integrationv1.Claim
	Validators  *integrationv1.Validators
	NotModified bool
	Meta        *integrationv1.ResponseMeta
}

// ChangeRequestList is one batched change-request read.
type ChangeRequestList struct {
	ChangeRequests []*integrationv1.ChangeRequest
	Meta           *integrationv1.ResponseMeta
}

// Evidence is one issue's trust evidence; unknown stays unknown, which
// the gate fails closed on (03j).
type Evidence struct {
	Number       int
	Editor       *integrationv1.Person
	LastEditedAt time.Time
	Labels       []*integrationv1.Label
}

// CallError carries an adapter's ProviderError across the seam, so the
// syncer maps health and back-off without parsing text (03j).
type CallError struct {
	Provider *integrationv1.ProviderError
}

// Error reports the provider failure.
func (e *CallError) Error() string {
	if e == nil || e.Provider == nil {
		return "sync: unknown provider error"
	}
	return "sync: " + e.Provider.GetMessage()
}

// validatorsFor builds the conditional-request validators the hub stored.
func validatorsFor(etag string, lastModifiedMs int64, hasLastMod bool) *integrationv1.Validators {
	if etag == "" && !hasLastMod {
		return nil
	}
	v := &integrationv1.Validators{Etag: etag}
	if hasLastMod {
		v.LastModifiedTime = timestamppb.New(time.UnixMilli(lastModifiedMs).UTC())
	}
	return v
}
