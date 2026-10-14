package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

// EvidenceQuery names the issues whose trust evidence the hub needs:
// the last title/body edit and, for the trusted labels only, the latest
// apply (03j). The hub asks per labelled item, not per item, so timeline
// lookups cost one query per labelled item.
type EvidenceQuery struct {
	Integration string
	Owner       string
	Repo        string
	Numbers     []int
	Labels      []string
}

// ItemEvidence is one issue's trust evidence: the last edit and the
// requested labels still applied with their latest apply. Unknown stays
// unknown (nil editor, no labels), which the gate fails closed on.
type ItemEvidence struct {
	Number       int
	Editor       *integrationv1.Person
	LastEditedAt time.Time
	Labels       []*integrationv1.Label
}

// EvidencePage carries the evidence with the GraphQL cost.
type EvidencePage struct {
	Items []ItemEvidence
	Meta  *integrationv1.ResponseMeta
}

// FillEvidence fetches editor and label-apply evidence for the numbered
// issues in one batched GraphQL call. An empty query makes no call.
func (c *Client) FillEvidence(ctx context.Context, q EvidenceQuery) (*EvidencePage, error) {
	out := &EvidencePage{}
	if len(q.Numbers) == 0 {
		return out, nil
	}
	if len(q.Numbers) > githubMaxBatchSize {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: q.Owner + "/" + q.Repo,
			Message: truncate(fmt.Sprintf("evidence batch of %d over the 50 per call", len(q.Numbers)), maxErrorMessage),
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
	var b bytes.Buffer
	b.WriteString("query { repo: repository(owner: " + strconv.Quote(q.Owner) + ", name: " + strconv.Quote(q.Repo) + ") { ")
	for _, n := range q.Numbers {
		fmt.Fprintf(&b, "i%d: issue(number: %d) { editor { login ... on User { databaseId } } lastEditedAt "+
			"timeline: timelineItems(itemTypes: [LABELED_EVENT, UNLABELED_EVENT], first: 100) { nodes { __typename "+
			"... on LabeledEvent { actor { login ... on User { databaseId } } createdAt label { name } } "+
			"... on UnlabeledEvent { actor { login ... on User { databaseId } } createdAt label { name } } } } } ",
			n, n)
	}
	b.WriteString("} rateLimit { limit cost remaining resetAt } }")
	payload, err := json.Marshal(map[string]string{"query": b.String()})
	if err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: q.Owner + "/" + q.Repo,
			Message: truncate("encode evidence query: "+err.Error(), maxErrorMessage),
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase()+"/graphql", bytes.NewReader(payload))
	if err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: q.Owner + "/" + q.Repo,
			Message: truncate(err.Error(), maxErrorMessage),
		}}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, &Error{Provider: unavailable(q.Owner+"/"+q.Repo, "graphql evidence: "+err.Error())}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return nil, &Error{Provider: unavailable(q.Owner+"/"+q.Repo, "read evidence: "+err.Error())}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{Provider: classifyStatus(resp.StatusCode, resp.Header, q.Owner+"/"+q.Repo,
			"POST evidence", resp.Status, truncate(string(raw), 256))}
	}
	var envelope evidenceEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, &Error{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Project: q.Owner + "/" + q.Repo,
			Message: truncate("decode evidence: "+err.Error(), maxErrorMessage),
		}}
	}
	out.Meta = &integrationv1.ResponseMeta{Cost: uint32Saturating(envelope.Data.RateLimit.Cost)}
	out.Meta.RateLimits = []*integrationv1.RateLimitState{
		graphQLRateLimit(envelope.Data.RateLimit.Limit, envelope.Data.RateLimit.Cost,
			envelope.Data.RateLimit.Remaining, envelope.Data.RateLimit.ResetAt),
	}
	byNumber := map[int]*evidenceIssueJSON{}
	for alias, issue := range envelope.Data.Repo {
		n, err := strconv.Atoi(strings.TrimPrefix(alias, "i"))
		if err != nil {
			continue
		}
		issue := issue
		byNumber[n] = &issue
	}
	for _, n := range q.Numbers {
		item := ItemEvidence{Number: n}
		issue := byNumber[n]
		if issue == nil {
			out.Items = append(out.Items, item)
			continue
		}
		item.Editor = issue.Editor.person()
		if t, err := time.Parse(time.RFC3339, issue.LastEditedAt); err == nil {
			item.LastEditedAt = t.UTC()
		}
		item.Labels = issue.appliedLabels(q.Labels)
		out.Items = append(out.Items, item)
	}
	return out, nil
}

type evidenceEnvelope struct {
	Data struct {
		// Aliased per-issue lookups (i7398, ...) decode as a map; the
		// caller maps aliases back to numbers.
		Repo      map[string]evidenceIssueJSON `json:"repo"`
		RateLimit struct {
			Limit     int    `json:"limit"`
			Cost      int    `json:"cost"`
			Remaining int    `json:"remaining"`
			ResetAt   string `json:"resetAt"`
		} `json:"rateLimit"`
	} `json:"data"`
}

type evidenceIssueJSON struct {
	Editor       actorJSON `json:"editor"`
	LastEditedAt string    `json:"lastEditedAt"`
	Timeline     struct {
		Nodes []struct {
			Type  string    `json:"__typename"`
			Actor actorJSON `json:"actor"`
			At    string    `json:"createdAt"`
			Label struct {
				Name string `json:"name"`
			} `json:"label"`
		} `json:"nodes"`
	} `json:"timeline"`
}

// appliedLabels replays the label timeline per requested label: a LABELED
// event sets the latest apply, an UNLABELED clears it, so a removed label
// carries no grant (03j). Only requested labels are reported.
func (e evidenceIssueJSON) appliedLabels(want []string) []*integrationv1.Label {
	type apply struct {
		by *integrationv1.Person
		at *timestamppb.Timestamp
	}
	state := map[string]*apply{}
	for _, n := range e.Timeline.Nodes {
		name := ""
		for _, w := range want {
			if strings.EqualFold(n.Label.Name, w) {
				name = w
				break
			}
		}
		if name == "" {
			continue
		}
		switch n.Type {
		case "LabeledEvent":
			at := parseTime(n.At)
			if n.Actor.Login == "" || at == nil {
				state[name] = nil
				continue
			}
			by := n.Actor.person()
			state[name] = &apply{by: by, at: at}
		case "UnlabeledEvent":
			delete(state, name)
		}
	}
	var out []*integrationv1.Label
	for _, w := range want {
		if a := state[w]; a != nil {
			out = append(out, &integrationv1.Label{Name: w, AppliedBy: a.by, AppliedTime: a.at})
		}
	}
	return out
}
