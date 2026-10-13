package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Bounds for one issues page (03m: 100 per page; 02b: whole messages under
// 1 MiB, so the page body is capped well below it).
const (
	issuesPerPage  = 100
	maxPageBytes   = 2 << 20
	maxTitleBytes  = 512
	cursorOverlap  = 30 * time.Second
	apiVersion     = "2022-11-28"
	defaultAPIBase = "https://api.github.com"
)

// Error is a failed adapter call: the ProviderError the syncer maps to
// health, back-off and triage (03j). Unwrap it for errors.As.
type Error struct {
	Provider *integrationv1.ProviderError
}

// Error reports the call.
func (e *Error) Error() string { return "github: " + e.Provider.GetMessage() }

// Unwrap returns the provider detail.
func (e *Error) Unwrap() error { return nil }

// Client is the typed REST wrapper over net/http (ADR 0020): few endpoints,
// with ETags and rate-limit headers under the adapter's own control.
// HTTP carries the transport (a test server or the default); Tokens mints
// the installation token per call. APIBase defaults to api.github.com.
type Client struct {
	HTTP    *http.Client
	APIBase string
	Tokens  TokenSource
	// ClaimMarker is the comment text the claim loop matches; empty means
	// DefaultClaimMarker.
	ClaimMarker string
}

// ListIssuesQuery is one poll of a repo's issues (service.proto
// ListWorkItemsRequest): the hub's stored cursor and validators, the page
// to fetch, and whether closes must be seen.
type ListIssuesQuery struct {
	Integration   string
	Owner         string
	Repo          string
	Since         time.Time
	Validators    *integrationv1.Validators
	IncludeClosed bool
	Page          int
}

// IssuePage is one parsed page: the mirror rows, this page's validators
// for the next poll, the next cursor on the last page, and the budgets the
// call touched. NotModified carries no items and changes nothing.
type IssuePage struct {
	Items       []*integrationv1.WorkItem
	Validators  *integrationv1.Validators
	NotModified bool
	NextSince   *integrationv1.ChangeCursor
	Meta        *integrationv1.ResponseMeta
}

// ListIssues polls one page of issues with conditional requests (03m W4).
// Pull-request entries filter out: change requests come from GraphQL.
// Label evidence (who applied the trusted label, when) is names only here;
// the batched timeline lookup fills it for the trusted labels.
func (c *Client) ListIssues(ctx context.Context, q ListIssuesQuery) (*IssuePage, error) {
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAUTHENTICATED,
			Message: truncate("installation token: "+err.Error(), maxErrorMessage),
			Project: q.Owner + "/" + q.Repo,
		}}
	}
	state := "open"
	if q.IncludeClosed {
		state = "all"
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	u := c.apiBase() + "/repos/" + url.PathEscape(q.Owner) + "/" + url.PathEscape(q.Repo) +
		"/issues?state=" + state + "&sort=updated&direction=asc&per_page=" + strconv.Itoa(issuesPerPage) +
		"&page=" + strconv.Itoa(page)
	// Time-only cursors are widened by a small overlap inside the adapter;
	// the hub's update_time guard drops the repeats (03j).
	if !q.Since.IsZero() {
		u += "&since=" + url.QueryEscape(q.Since.Add(-cursorOverlap).UTC().Format(time.RFC3339))
	}
	resp, err := c.doGet(ctx, token, u, q.Validators)
	if err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:           integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAVAILABLE,
			Retryable:      true,
			Project:        q.Owner + "/" + q.Repo,
			ProviderStatus: "transport",
			Message:        truncate("GET issues: "+err.Error(), maxErrorMessage),
		}}
	}
	defer func() { _ = resp.Body.Close() }()
	limits, providerTime := parseRateLimits(resp.Header, integrationv1.RateLimitKind_RATE_LIMIT_KIND_CORE)
	meta := &integrationv1.ResponseMeta{RateLimits: limits}
	if !providerTime.IsZero() {
		meta.ProviderTime = timestamppb.New(providerTime)
	}
	project := q.Owner + "/" + q.Repo
	if resp.StatusCode == http.StatusNotModified {
		return &IssuePage{NotModified: true, Validators: q.Validators, Meta: meta}, nil
	}
	validators := &integrationv1.Validators{Etag: resp.Header.Get("ETag")}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := time.Parse(http.TimeFormat, lm); err == nil {
			validators.LastModifiedTime = timestamppb.New(t.UTC())
		}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &Error{Provider: classifyStatus(resp.StatusCode, resp.Header, project,
			"GET "+project+"/issues", resp.Status, truncate(string(body), 256))}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, &Error{Provider: unavailable(project, "read issues page: "+err.Error())}
	}
	if len(raw) > maxPageBytes {
		return nil, &Error{Provider: unavailable(project, "issues page over its cap")}
	}
	var entries []issueJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, &Error{Provider: unsupported(q, "decode issues page: "+err.Error())}
	}
	page2 := &IssuePage{Validators: validators, Meta: meta}
	maxTime := q.Since
	for _, e := range entries {
		if e.PullRequest != nil || e.Number <= 0 {
			continue
		}
		item := e.workItem(q.Integration, project)
		page2.Items = append(page2.Items, item)
		if t := item.GetUpdateTime(); t != nil && t.AsTime().After(maxTime) {
			maxTime = t.AsTime()
		}
	}
	page2.NextSince = &integrationv1.ChangeCursor{UpdatedAfterTime: timestamppb.New(maxTime.UTC())}
	return page2, nil
}

// doGet sends one authenticated conditional GET.
func (c *Client) doGet(ctx context.Context, token, rawURL string, validators *integrationv1.Validators) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("Authorization", "Bearer "+token)
	if validators != nil {
		if etag := validators.GetEtag(); etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		if lm := validators.GetLastModifiedTime(); lm != nil {
			req.Header.Set("If-Modified-Since", lm.AsTime().UTC().Format(http.TimeFormat))
		}
	}
	return c.http().Do(req)
}

// maxIssueBody is the item-detail text cap (service.proto GetWorkItem:
// bodies are capped at 64 KiB).
const maxIssueBody = 64 << 10

// IssueDetail is one issue with its body: the item-detail read behind
// GetWorkItem (03m W4), ETagged like the list.
type IssueDetail struct {
	Item        *integrationv1.WorkItem
	Body        string
	Validators  *integrationv1.Validators
	NotModified bool
	Meta        *integrationv1.ResponseMeta
}

// GetIssue fetches one issue by number with conditional requests. The
// row-12 read API serves bodies from here on demand; the mirror keeps
// only the size.
func (c *Client) GetIssue(ctx context.Context, integration, owner, repo string, number int, validators *integrationv1.Validators) (*IssueDetail, error) {
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAUTHENTICATED,
			Message: truncate("installation token: "+err.Error(), maxErrorMessage),
			Project: owner + "/" + repo,
		}}
	}
	u := c.apiBase() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) +
		"/issues/" + strconv.Itoa(number)
	resp, err := c.doGet(ctx, token, u, validators)
	if err != nil {
		return nil, &Error{Provider: unavailable(owner+"/"+repo, "GET issue: "+err.Error())}
	}
	defer func() { _ = resp.Body.Close() }()
	limits, providerTime := parseRateLimits(resp.Header, integrationv1.RateLimitKind_RATE_LIMIT_KIND_CORE)
	meta := &integrationv1.ResponseMeta{RateLimits: limits}
	if !providerTime.IsZero() {
		meta.ProviderTime = timestamppb.New(providerTime)
	}
	project := owner + "/" + repo
	if resp.StatusCode == http.StatusNotModified {
		return &IssueDetail{NotModified: true, Validators: validators, Meta: meta}, nil
	}
	out := &IssueDetail{Validators: &integrationv1.Validators{Etag: resp.Header.Get("ETag")}, Meta: meta}
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := time.Parse(http.TimeFormat, lm); err == nil {
			out.Validators.LastModifiedTime = timestamppb.New(t.UTC())
		}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &Error{Provider: classifyStatus(resp.StatusCode, resp.Header, project,
			"GET "+project+"/issues/"+strconv.Itoa(number), resp.Status, truncate(string(body), 256))}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, &Error{Provider: unavailable(project, "read issue: "+err.Error())}
	}
	if len(raw) > maxPageBytes {
		return nil, &Error{Provider: unavailable(project, "issue over its cap")}
	}
	var entry issueJSON
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: project,
			Message: truncate("decode issue: "+err.Error(), maxErrorMessage),
		}}
	}
	out.Item = entry.workItem(integration, project)
	out.Body = cleanText(entry.Body, maxIssueBody)
	return out, nil
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return c.APIBase
	}
	return defaultAPIBase
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func unsupported(q ListIssuesQuery, detail string) *integrationv1.ProviderError {
	return &integrationv1.ProviderError{
		Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
		Project: q.Owner + "/" + q.Repo,
		Message: truncate(detail, maxErrorMessage),
	}
}

func unavailable(project, detail string) *integrationv1.ProviderError {
	return &integrationv1.ProviderError{
		Kind:      integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAVAILABLE,
		Retryable: true,
		Project:   project,
		Message:   truncate(detail, maxErrorMessage),
	}
}

// issueJSON is the REST issue shape the adapter reads. Bodies ride along
// but only their size is mirrored; GetWorkItem fetches the text on demand.
type issueJSON struct {
	Number      int            `json:"number"`
	Title       string         `json:"title"`
	User        accountJSON    `json:"user"`
	Labels      []labelJSON    `json:"labels"`
	State       string         `json:"state"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
	Body        string         `json:"body"`
	Assignees   []accountJSON  `json:"assignees"`
	PullRequest map[string]any `json:"pull_request"`
}

type accountJSON struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type labelJSON struct {
	Name string `json:"name"`
}

func (e issueJSON) workItem(integration, project string) *integrationv1.WorkItem {
	state := integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN
	if e.State == "closed" {
		state = integrationv1.WorkItemState_WORK_ITEM_STATE_CLOSED
	}
	item := &integrationv1.WorkItem{
		Ref: &typev1.WorkItemRef{
			Key: &typev1.WorkItemKey{
				Integration: &typev1.IntegrationId{Value: integration},
				Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
				Project:     project,
				Key:         strconv.Itoa(e.Number),
			},
			Provider: typev1.ProviderKind_PROVIDER_KIND_GITHUB,
			Url:      fmt.Sprintf("https://github.com/%s/issues/%d", project, e.Number),
			Display:  fmt.Sprintf("%s#%d", project, e.Number),
		},
		Title:         cleanText(e.Title, maxTitleBytes),
		State:         state,
		ProviderState: cleanText(e.State, 64),
		Author:        e.User.person(),
		BodyBytes:     uint64(len(e.Body)),
		CreateTime:    parseTime(e.CreatedAt),
		UpdateTime:    parseTime(e.UpdatedAt),
	}
	for _, l := range e.Labels {
		if l.Name == "" {
			continue
		}
		item.Labels = append(item.Labels, &integrationv1.Label{Name: cleanText(l.Name, 64)})
	}
	for _, a := range e.Assignees {
		if p := a.person(); p != nil {
			item.Assignees = append(item.Assignees, p)
		}
	}
	return item
}

func (a accountJSON) person() *integrationv1.Person {
	if a.Login == "" {
		return nil
	}
	p := &integrationv1.Person{Login: cleanText(a.Login, 39)}
	if a.ID != 0 {
		p.ProviderId = strconv.FormatInt(a.ID, 10)
	}
	return p
}

func parseTime(raw string) *timestamppb.Timestamp {
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return timestamppb.New(t.UTC())
}
