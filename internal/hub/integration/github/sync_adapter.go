package github

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/syncer"
)

// Adapter adapts the GitHub client to the syncer's provider-neutral
// polling surface (03j: the hub owns the loop, the adapter keeps no sync
// state). Project paths are "owner/repo"; FleetUsers names the fleet's own
// provider logins for by_fleet.
type Adapter struct {
	C           *Client
	Integration string
	FleetUsers  map[string]bool
}

// SplitProject cuts an "owner/repo" project path.
func SplitProject(project string) (owner, repo string, ok bool) {
	owner, repo, ok = strings.Cut(project, "/")
	return owner, repo, ok && owner != "" && repo != ""
}

// Describe reports the adapter's capabilities.
func (a *Adapter) Describe() *integrationv1.IntegrationCapabilities { return Describe() }

// ListWorkItems polls one page of a repo's issues.
func (a *Adapter) ListWorkItems(ctx context.Context, project string, since time.Time, validators *integrationv1.Validators, includeClosed bool) (*syncer.WorkItemPage, error) {
	owner, repo, ok := SplitProject(project)
	if !ok {
		return nil, unsupportedProject(project)
	}
	page, err := a.C.ListIssues(ctx, ListIssuesQuery{
		Integration: a.Integration, Owner: owner, Repo: repo,
		Since: since, Validators: validators, IncludeClosed: includeClosed,
	})
	if err != nil {
		return nil, convertError(err)
	}
	return &syncer.WorkItemPage{
		Items: page.Items, Validators: page.Validators,
		NotModified: page.NotModified, NextSince: page.NextSince, Meta: page.Meta,
	}, nil
}

// ListClaims returns an issue's full marker-comment list.
func (a *Adapter) ListClaims(ctx context.Context, project, itemKey string, validators *integrationv1.Validators) (*syncer.ClaimPage, error) {
	owner, repo, ok := SplitProject(project)
	if !ok {
		return nil, unsupportedProject(project)
	}
	number, err := strconv.Atoi(itemKey)
	if err != nil {
		return nil, &syncer.CallError{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: project,
			Message: "claim lookup needs an issue number, got " + itemKey,
		}}
	}
	page, err := a.C.ListClaims(ctx, ClaimQuery{
		Integration: a.Integration, Owner: owner, Repo: repo, Number: number,
		Validators: validators, FleetUsers: a.FleetUsers,
	})
	if err != nil {
		return nil, convertError(err)
	}
	return &syncer.ClaimPage{
		Claims: page.Claims, Validators: page.Validators,
		NotModified: page.NotModified, Meta: page.Meta,
	}, nil
}

// ListChangeRequests finds change requests by head branch, head sha or
// linked issue key ("" skips that lookup).
func (a *Adapter) ListChangeRequests(ctx context.Context, project string, branches, commits []string, linkedKey string) (*syncer.ChangeRequestList, error) {
	owner, repo, ok := SplitProject(project)
	if !ok {
		return nil, unsupportedProject(project)
	}
	q := ChangeRequestQuery{
		Integration: a.Integration, Owner: owner, Repo: repo,
		SourceBranches: branches, HeadCommits: commits,
	}
	if linkedKey != "" {
		q.LinkedWorkItem = &typev1.WorkItemKey{
			Integration: &typev1.IntegrationId{Value: a.Integration},
			Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
			Project:     project,
			Key:         linkedKey,
		}
	}
	out, err := a.C.ListChangeRequests(ctx, q)
	if err != nil {
		return nil, convertError(err)
	}
	return &syncer.ChangeRequestList{ChangeRequests: out.ChangeRequests, Meta: out.Meta}, nil
}

// GetWorkItem fetches one issue with its body for the row-12 read API.
func (a *Adapter) GetWorkItem(ctx context.Context, project, key string, validators *integrationv1.Validators) (*IssueDetail, error) {
	owner, repo, ok := SplitProject(project)
	if !ok {
		return nil, unsupportedProject(project)
	}
	number, err := strconv.Atoi(key)
	if err != nil {
		return nil, &syncer.CallError{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: project,
			Message: "item lookup needs an issue number, got " + key,
		}}
	}
	out, err := a.C.GetIssue(ctx, a.Integration, owner, repo, number, validators)
	if err != nil {
		return nil, convertError(err)
	}
	return out, nil
}

// FillEvidence fetches editor and label-apply evidence for the numbered
// issues.
func (a *Adapter) FillEvidence(ctx context.Context, project string, numbers []int, labels []string) ([]syncer.Evidence, *integrationv1.ResponseMeta, error) {
	owner, repo, ok := SplitProject(project)
	if !ok {
		return nil, nil, unsupportedProject(project)
	}
	page, err := a.C.FillEvidence(ctx, EvidenceQuery{
		Integration: a.Integration, Owner: owner, Repo: repo, Numbers: numbers, Labels: labels,
	})
	if err != nil {
		return nil, nil, convertError(err)
	}
	out := make([]syncer.Evidence, 0, len(page.Items))
	for _, item := range page.Items {
		out = append(out, syncer.Evidence{
			Number: item.Number, Editor: item.Editor,
			LastEditedAt: item.LastEditedAt, Labels: item.Labels,
		})
	}
	return out, page.Meta, nil
}

func unsupportedProject(project string) *syncer.CallError {
	return &syncer.CallError{Provider: &integrationv1.ProviderError{
		Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
		Project: project,
		Message: fmt.Sprintf("project %q is not owner/repo", project),
	}}
}

// convertError carries the adapter's ProviderError across the seam.
func convertError(err error) error {
	if err == nil {
		return nil
	}
	var call *Error
	if errors.As(err, &call) {
		return &syncer.CallError{Provider: call.Provider}
	}
	return err
}
