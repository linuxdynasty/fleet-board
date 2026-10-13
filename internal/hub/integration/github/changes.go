package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// githubMaxBatchSize is the branches-plus-commits ceiling of one
// ListChangeRequests call (service.proto: at most
// capabilities.max_batch_size, 50 for GitHub GraphQL). The hub sends one
// value per request when the adapter declares 0.
const githubMaxBatchSize = 50

// maxBodyParse is how much PR body the Fixes-keyword scan reads (service
// proto: bodies are capped at 64 KiB).
const maxBodyParse = 64 << 10

// ChangeRequestQuery is one verifier or poll read (service.proto
// ListChangeRequestsRequest): the branches agents report in
// BranchState.branch, the head shas of verifying goals, or the work item
// whose closing PRs are the fallback (03m). States empty means all states.
type ChangeRequestQuery struct {
	Integration    string
	Owner          string
	Repo           string
	SourceBranches []string
	HeadCommits    []string
	LinkedWorkItem *typev1.WorkItemKey
}

// ChangeRequestPage is the mapped change requests with the GraphQL cost.
type ChangeRequestPage struct {
	ChangeRequests []*integrationv1.ChangeRequest
	Meta           *integrationv1.ResponseMeta
}

// ListChangeRequests finds change requests by head ref, head sha or linked
// work item in one batched GraphQL call (03m W4, W6). The link needs no
// naming convention: the branch the agent reports is looked up directly,
// and forks match by name with head_repo compared before trusting (03j).
func (c *Client) ListChangeRequests(ctx context.Context, q ChangeRequestQuery) (*ChangeRequestPage, error) {
	if len(q.SourceBranches)+len(q.HeadCommits) > githubMaxBatchSize {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: q.Owner + "/" + q.Repo,
			Message: truncate(fmt.Sprintf("change requests batch of %d over the 50 per call", len(q.SourceBranches)+len(q.HeadCommits)), maxErrorMessage),
		}}
	}
	if len(q.SourceBranches) == 0 && len(q.HeadCommits) == 0 && q.LinkedWorkItem == nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: q.Owner + "/" + q.Repo,
			Message: "change requests need a branch, a head commit or a linked work item",
		}}
	}
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAUTHENTICATED,
			Message: truncate("installation token: "+err.Error(), maxErrorMessage),
			Project: q.Owner + "/" + q.Repo,
		}}
	}
	query := buildChangeRequestQuery(q)
	payload, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, &Error{Provider: unsupportedChange(q, "encode change request query: "+err.Error())}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase()+"/graphql", bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{Provider: unsupportedChange(q, err.Error())}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, &Error{Provider: unavailable(q.Owner+"/"+q.Repo, "graphql change requests: "+err.Error())}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, &Error{Provider: unavailable(q.Owner+"/"+q.Repo, "read change requests: "+err.Error())}
	}
	if len(raw) > maxPageBytes {
		return nil, &Error{Provider: unavailable(q.Owner+"/"+q.Repo, "change requests over their cap")}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Provider: classifyStatus(resp.StatusCode, resp.Header, q.Owner+"/"+q.Repo,
			"POST change requests", resp.Status, truncate(string(raw), 256))}
	}
	var envelope graphQLEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, &Error{Provider: unsupportedChange(q, "decode change requests: "+err.Error())}
	}
	if len(envelope.Errors) > 0 {
		first := envelope.Errors[0]
		return nil, &Error{Provider: graphQLError(first.Type, first.Message, q.Owner+"/"+q.Repo)}
	}
	meta := &integrationv1.ResponseMeta{Cost: uint32Saturating(envelope.Data.RateLimit.Cost)}
	meta.RateLimits = []*integrationv1.RateLimitState{
		graphQLRateLimit(envelope.Data.RateLimit.Limit, envelope.Data.RateLimit.Cost,
			envelope.Data.RateLimit.Remaining, envelope.Data.RateLimit.ResetAt),
	}
	out := &ChangeRequestPage{Meta: meta}
	seen := map[int]bool{}
	add := func(pr pullRequestJSON) {
		if pr.Number == 0 || seen[pr.Number] {
			return
		}
		seen[pr.Number] = true
		out.ChangeRequests = append(out.ChangeRequests, pr.changeRequest(q.Integration, q.Owner, q.Repo))
	}
	for _, section := range envelope.Data.Repo.Branches {
		for _, pr := range section.Nodes {
			add(pr)
		}
	}
	for _, section := range envelope.Data.Repo.Commits {
		for _, pr := range section.Associated.Nodes {
			add(pr)
		}
	}
	for _, edge := range envelope.Data.Linked.Edges {
		add(edge.Node)
	}
	return out, nil
}

func unsupportedChange(q ChangeRequestQuery, detail string) *integrationv1.ProviderError {
	return &integrationv1.ProviderError{
		Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
		Project: q.Owner + "/" + q.Repo,
		Message: truncate(detail, maxErrorMessage),
	}
}

// prFragment is the one PR shape every lookup shares, so branch, commit
// and linked reads report the same fields.
const prFragment = `number title state isDraft mergedAt closedAt updatedAt
	headRefName headRefOid baseRefName
	headRepository { nameWithOwner } baseRepository { nameWithOwner }
	author { login ... on User { databaseId } }
	mergedBy { login ... on User { databaseId } }
	mergeStateStatus reviewDecision body
	commits(first: 100) { nodes { commit { oid committedDate statusCheckRollup { state } } } }
	reviews(first: 100, orderBy: {field: SUBMITTED_AT, direction: ASC}) { nodes { author { login ... on User { databaseId } } state submittedAt } }
	closingIssuesReferences(first: 20) { edges { node { number } } }`

// buildChangeRequestQuery batches the branch, commit and linked lookups in
// one call (03m: up to max_batch_size per call, so N goals verify in ~N/50
// calls, not N).
func buildChangeRequestQuery(q ChangeRequestQuery) string {
	var b bytes.Buffer
	b.WriteString("query { repo: repository(owner: " + strconv.Quote(q.Owner) + ", name: " + strconv.Quote(q.Repo) + ") { ")
	for i, branch := range q.SourceBranches {
		fmt.Fprintf(&b, "b%d: pullRequests(headRefName: %s, first: 5, orderBy: {field: UPDATED_AT, direction: DESC}) { nodes { %s } } ",
			i, strconv.Quote(branch), prFragment)
	}
	for i, sha := range q.HeadCommits {
		fmt.Fprintf(&b, "c%d: object(oid: %s) { ... on Commit { associatedPullRequests(first: 5) { nodes { %s } } } } ",
			i, strconv.Quote(sha), prFragment)
	}
	b.WriteString("} ")
	if q.LinkedWorkItem != nil {
		fmt.Fprintf(&b, "linked: search(query: %s, type: ISSUE, first: 10) { edges { node { ... on PullRequest { %s } } } } ",
			strconv.Quote(fmt.Sprintf("repo:%s/%s type:pr %s in:body", q.Owner, q.Repo, q.LinkedWorkItem.GetKey())), prFragment)
	}
	b.WriteString("rateLimit { limit cost remaining resetAt } }")
	return b.String()
}
