package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

// DefaultClaimMarker is the comment text the claim loop looks for (03m:
// the eshu-issue-driver claim comment). It is configurable per Client for
// fleets whose drivers word it differently; when the pattern changes,
// claims stop parsing and claims_seen_total drops to 0, which warns (03m).
const DefaultClaimMarker = "eshu-issue-driver claim"

// maxClaimBytes caps one claim's mirrored text (model.proto: 4 KiB).
const maxClaimBytes = 4 << 10

// ClaimQuery is one item's claim poll (03m: every 5 min per candidate work
// item): the comments cursor and validators the hub stored, and the fleet's
// own usernames (ForgeCliStatus.user of every machine) for by_fleet.
type ClaimQuery struct {
	Integration string
	Owner       string
	Repo        string
	Number      int
	Since       time.Time
	Validators  *integrationv1.Validators
	Page        int
	FleetUsers  map[string]bool
}

// ClaimPage is the matched claims with the page's validators and budgets.
type ClaimPage struct {
	Claims      []*integrationv1.Claim
	Validators  *integrationv1.Validators
	NotModified bool
	NextSince   *integrationv1.ChangeCursor
	Meta        *integrationv1.ResponseMeta
}

// ListClaims polls an issue's comments for claim markers with conditional
// requests. Only marker comments count; review chatter does not. A comment
// by one of the fleet's users is by_fleet; any other marker is a foreign
// claim, which parks the item CLAIMED_ELSEWHERE at upsert.
func (c *Client) ListClaims(ctx context.Context, q ClaimQuery) (*ClaimPage, error) {
	marker := c.ClaimMarker
	if marker == "" {
		marker = DefaultClaimMarker
	}
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAUTHENTICATED,
			Message: truncate("installation token: "+err.Error(), maxErrorMessage),
			Project: q.Owner + "/" + q.Repo,
		}}
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	u := c.apiBase() + "/repos/" + url.PathEscape(q.Owner) + "/" + url.PathEscape(q.Repo) +
		"/issues/" + strconv.Itoa(q.Number) + "/comments?sort=updated&direction=asc&per_page=" +
		strconv.Itoa(issuesPerPage) + "&page=" + strconv.Itoa(page)
	if !q.Since.IsZero() {
		u += "&since=" + url.QueryEscape(q.Since.Add(-cursorOverlap).UTC().Format(time.RFC3339))
	}
	resp, err := c.doGet(ctx, token, u, q.Validators)
	if err != nil {
		return nil, &Error{Provider: unavailable(q.Owner+"/"+q.Repo, "GET claims: "+err.Error())}
	}
	defer func() { _ = resp.Body.Close() }()
	limits, providerTime := parseRateLimits(resp.Header, integrationv1.RateLimitKind_RATE_LIMIT_KIND_CORE)
	meta := &integrationv1.ResponseMeta{RateLimits: limits}
	if !providerTime.IsZero() {
		meta.ProviderTime = timestamppb.New(providerTime)
	}
	project := q.Owner + "/" + q.Repo
	if resp.StatusCode == http.StatusNotModified {
		return &ClaimPage{NotModified: true, Validators: q.Validators, Meta: meta}, nil
	}
	validators := &integrationv1.Validators{Etag: resp.Header.Get("ETag")}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &Error{Provider: classifyStatus(resp.StatusCode, resp.Header, project,
			"GET "+project+" claims", resp.Status, truncate(string(body), 256))}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, &Error{Provider: unavailable(project, "read claims page: "+err.Error())}
	}
	if len(raw) > maxPageBytes {
		return nil, &Error{Provider: unavailable(project, "claims page over its cap")}
	}
	var entries []commentJSON
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: project,
			Message: truncate("decode claims page: "+err.Error(), maxErrorMessage),
		}}
	}
	out := &ClaimPage{Validators: validators, Meta: meta}
	maxTime := q.Since
	for _, e := range entries {
		if !strings.Contains(e.Body, marker) {
			continue
		}
		by := e.User.person()
		if by == nil {
			continue
		}
		claim := &integrationv1.Claim{
			Mechanism: integrationv1.ClaimMechanism_CLAIM_MECHANISM_COMMENT,
			By:        by,
			ClaimTime: parseTime(e.CreatedAt),
			Text:      cleanText(e.Body, maxClaimBytes),
			ByFleet:   matchUser(q.FleetUsers, by.GetLogin()),
		}
		out.Claims = append(out.Claims, claim)
		if t := claim.GetClaimTime(); t != nil && t.AsTime().After(maxTime) {
			maxTime = t.AsTime()
		}
	}
	out.NextSince = &integrationv1.ChangeCursor{UpdatedAfterTime: timestamppb.New(maxTime.UTC())}
	return out, nil
}

// matchUser compares a login case-insensitively: provider logins are the
// fleet's usernames in any case (03j).
func matchUser(set map[string]bool, login string) bool {
	for k, v := range set {
		if v && strings.EqualFold(k, login) {
			return true
		}
	}
	return false
}

type commentJSON struct {
	User      accountJSON `json:"user"`
	CreatedAt string      `json:"created_at"`
	Body      string      `json:"body"`
}
