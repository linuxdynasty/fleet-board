package github

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

type branchSection struct {
	Nodes []pullRequestJSON `json:"nodes"`
}

type graphQLEnvelope struct {
	Data struct {
		Repo   repoSections `json:"repo"`
		Linked struct {
			Edges []struct {
				Node pullRequestJSON `json:"node"`
			} `json:"edges"`
		} `json:"linked"`
		RateLimit struct {
			Limit     int    `json:"limit"`
			Cost      int    `json:"cost"`
			Remaining int    `json:"remaining"`
			ResetAt   string `json:"resetAt"`
		} `json:"rateLimit"`
	} `json:"data"`
	Errors []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
}

type commitLookupJSON struct {
	Associated struct {
		Nodes []pullRequestJSON `json:"nodes"`
	} `json:"associatedPullRequests"`
}

// repoSections decodes the batched lookups, whose aliases (b0 for branches,
// c0 for commits) are dynamic. Keys decode in alias order so branch results
// come before commit results, deterministically.
type repoSections struct {
	Branches []branchSection
	Commits  []commitLookupJSON
}

// UnmarshalJSON decodes the alias map in sorted key order.
func (r *repoSections) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case strings.HasPrefix(k, "b"):
			var s branchSection
			if err := json.Unmarshal(m[k], &s); err != nil {
				return fmt.Errorf("github: decode branch lookup %s: %w", k, err)
			}
			r.Branches = append(r.Branches, s)
		case strings.HasPrefix(k, "c"):
			var s commitLookupJSON
			if err := json.Unmarshal(m[k], &s); err != nil {
				return fmt.Errorf("github: decode commit lookup %s: %w", k, err)
			}
			r.Commits = append(r.Commits, s)
		}
	}
	return nil
}

type pullRequestJSON struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	State       string `json:"state"`
	IsDraft     bool   `json:"isDraft"`
	MergedAt    string `json:"mergedAt"`
	ClosedAt    string `json:"closedAt"`
	UpdatedAt   string `json:"updatedAt"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	HeadRepo    struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
	BaseRepo struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"baseRepository"`
	Author         actorJSON `json:"author"`
	MergedBy       actorJSON `json:"mergedBy"`
	MergeState     string    `json:"mergeStateStatus"`
	ReviewDecision string    `json:"reviewDecision"`
	Body           string    `json:"body"`
	Commits        struct {
		Nodes []struct {
			Commit struct {
				Oid         string `json:"oid"`
				CommittedAt string `json:"committedDate"`
				Rollup      *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	Reviews struct {
		Nodes []struct {
			Author      actorJSON `json:"author"`
			State       string    `json:"state"`
			SubmittedAt string    `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
	ClosingRefs struct {
		Edges []struct {
			Node struct {
				Number int `json:"number"`
			} `json:"node"`
		} `json:"edges"`
	} `json:"closingIssuesReferences"`
}

type actorJSON struct {
	Login      string `json:"login"`
	DatabaseID int64  `json:"databaseId"`
}

func (a actorJSON) person() *integrationv1.Person {
	if a.Login == "" {
		return nil
	}
	p := &integrationv1.Person{Login: cleanText(a.Login, 39)}
	if a.DatabaseID != 0 {
		p.ProviderId = strconv.FormatInt(a.DatabaseID, 10)
	}
	return p
}

// fixesPattern finds closing keywords ("Fixes #n") in the body, next to
// closingIssuesReferences (03m).
var fixesPattern = regexp.MustCompile(`(?i)\b(?:clos(?:e|es|ed|ing)|fix(?:|es|ed)|resolv(?:e|es|ed|ing))\s+#(\d+)`)

func (pr pullRequestJSON) changeRequest(integration, owner, repo string) *integrationv1.ChangeRequest {
	project := owner + "/" + repo
	state := integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN
	switch {
	case pr.State == "MERGED" || pr.MergedAt != "":
		state = integrationv1.WorkItemState_WORK_ITEM_STATE_MERGED
	case pr.State == "CLOSED":
		state = integrationv1.WorkItemState_WORK_ITEM_STATE_CLOSED
	case pr.IsDraft:
		state = integrationv1.WorkItemState_WORK_ITEM_STATE_DRAFT
	}
	out := &integrationv1.ChangeRequest{
		Ref: &typev1.WorkItemRef{
			Key: &typev1.WorkItemKey{
				Integration: &typev1.IntegrationId{Value: integration},
				Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_CHANGE_REQUEST,
				Project:     project,
				Key:         strconv.Itoa(pr.Number),
			},
			Provider: typev1.ProviderKind_PROVIDER_KIND_GITHUB,
			Url:      fmt.Sprintf("https://github.com/%s/pull/%d", project, pr.Number),
			Display:  fmt.Sprintf("%s#%d", project, pr.Number),
		},
		Repo:           &typev1.RepoRef{Host: "github.com", Path: project},
		Title:          cleanText(pr.Title, maxTitleBytes),
		State:          state,
		ProviderState:  cleanText(pr.State, 64),
		SourceBranch:   cleanText(pr.HeadRefName, 256),
		TargetBranch:   cleanText(pr.BaseRefName, 256),
		HeadCommit:     cleanText(pr.HeadRefOid, 64),
		Checks:         &integrationv1.CheckSummary{Rollup: pr.rollup()},
		MergeState:     mapMergeState(pr.MergeState),
		Author:         pr.Author.person(),
		UpdateTime:     parseTime(pr.UpdatedAt),
		MergedTime:     parseTime(pr.MergedAt),
		ClosedTime:     parseTime(pr.ClosedAt),
		ReviewDecision: mapReviewDecision(pr.ReviewDecision),
		MergedBy:       pr.MergedBy.person(),
	}
	if pr.HeadRepo.NameWithOwner != "" && pr.HeadRepo.NameWithOwner != project {
		out.HeadRepo = &typev1.RepoRef{Host: "github.com", Path: pr.HeadRepo.NameWithOwner}
	}
	for _, r := range pr.Reviews.Nodes {
		review := &integrationv1.Review{By: r.Author.person(), ReviewTime: parseTime(r.SubmittedAt)}
		switch r.State {
		case "APPROVED":
			review.State = integrationv1.ReviewState_REVIEW_STATE_APPROVED
		case "CHANGES_REQUESTED":
			review.State = integrationv1.ReviewState_REVIEW_STATE_CHANGES_REQUESTED
		case "PENDING":
			continue
		default:
			review.State = integrationv1.ReviewState_REVIEW_STATE_COMMENTED
		}
		out.Reviews = append(out.Reviews, review)
	}
	out.ReviewRounds = pr.reviewRounds()
	out.LinkedWorkItems = pr.linkedItems(integration, project)
	return out
}

// rollup is the CI result about the head commit: SUCCESS only while every
// check in scope passes (model.proto: never SUCCESS on pending or missing).
func (pr pullRequestJSON) rollup() integrationv1.CheckRollup {
	for _, n := range pr.Commits.Nodes {
		if n.Commit.Oid != pr.HeadRefOid {
			continue
		}
		if n.Commit.Rollup == nil {
			return integrationv1.CheckRollup_CHECK_ROLLUP_NONE
		}
		switch n.Commit.Rollup.State {
		case "SUCCESS":
			return integrationv1.CheckRollup_CHECK_ROLLUP_SUCCESS
		case "FAILURE", "ERROR":
			return integrationv1.CheckRollup_CHECK_ROLLUP_FAILURE
		case "PENDING", "EXPECTED":
			return integrationv1.CheckRollup_CHECK_ROLLUP_PENDING
		}
		return integrationv1.CheckRollup_CHECK_ROLLUP_UNSPECIFIED
	}
	return integrationv1.CheckRollup_CHECK_ROLLUP_NONE
}

// reviewRounds counts CHANGES_REQUESTED reviews each followed by a new
// commit; it never resets, and failed CI is not a round (03m).
func (pr pullRequestJSON) reviewRounds() uint32 {
	var commits []time.Time
	for _, n := range pr.Commits.Nodes {
		if t, err := time.Parse(time.RFC3339, n.Commit.CommittedAt); err == nil {
			commits = append(commits, t)
		}
	}
	sort.Slice(commits, func(i, j int) bool { return commits[i].Before(commits[j]) })
	var rounds uint32
	for _, r := range pr.Reviews.Nodes {
		if r.State != "CHANGES_REQUESTED" {
			continue
		}
		submitted, err := time.Parse(time.RFC3339, r.SubmittedAt)
		if err != nil {
			continue
		}
		for _, c := range commits {
			if c.After(submitted) {
				rounds++
				break
			}
		}
	}
	return rounds
}

// linkedItems are the work items this request closes: the provider's
// closing references plus Fixes-keywords in the body (03m).
func (pr pullRequestJSON) linkedItems(integration, project string) []*typev1.WorkItemRef {
	var out []*typev1.WorkItemRef
	seen := map[string]bool{}
	add := func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, &typev1.WorkItemRef{
			Key: &typev1.WorkItemKey{
				Integration: &typev1.IntegrationId{Value: integration},
				Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
				Project:     project,
				Key:         key,
			},
			Provider: typev1.ProviderKind_PROVIDER_KIND_GITHUB,
			Display:  fmt.Sprintf("%s#%s", project, key),
		})
	}
	for _, e := range pr.ClosingRefs.Edges {
		if e.Node.Number != 0 {
			add(strconv.Itoa(e.Node.Number))
		}
	}
	body := pr.Body
	if len(body) > maxBodyParse {
		body = body[:maxBodyParse]
	}
	for _, m := range fixesPattern.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	return out
}

func mapReviewDecision(raw string) integrationv1.ReviewDecision {
	switch raw {
	case "APPROVED":
		return integrationv1.ReviewDecision_REVIEW_DECISION_APPROVED
	case "REVIEW_REQUIRED":
		return integrationv1.ReviewDecision_REVIEW_DECISION_REQUIRED
	case "CHANGES_REQUESTED":
		return integrationv1.ReviewDecision_REVIEW_DECISION_CHANGES_REQUESTED
	}
	return integrationv1.ReviewDecision_REVIEW_DECISION_UNSPECIFIED
}

func mapMergeState(raw string) integrationv1.MergeState {
	switch raw {
	case "CLEAN":
		return integrationv1.MergeState_MERGE_STATE_CLEAN
	case "BLOCKED":
		return integrationv1.MergeState_MERGE_STATE_BLOCKED
	case "DIRTY":
		return integrationv1.MergeState_MERGE_STATE_CONFLICTING
	case "BEHIND":
		return integrationv1.MergeState_MERGE_STATE_BEHIND
	case "UNSTABLE":
		return integrationv1.MergeState_MERGE_STATE_UNSTABLE
	case "UNKNOWN":
		return integrationv1.MergeState_MERGE_STATE_UNKNOWN
	}
	return integrationv1.MergeState_MERGE_STATE_UNSPECIFIED
}
