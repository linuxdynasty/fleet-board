package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

func graphQLServer(t *testing.T, check func(query string)) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "change_requests.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture any
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			t.Errorf("path = %s, want /graphql", r.URL.Path)
		}
		var req struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode query: %v", err)
		}
		if check != nil {
			check(req.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fixture)
	}))
}

func TestListChangeRequestsByBranch(t *testing.T) {
	t.Parallel()
	srv := graphQLServer(t, func(query string) {
		if !strings.Contains(query, "headRefName") || !strings.Contains(query, "fleet/eshu-7398") {
			t.Errorf("query looks up the agent-reported branch, got:\n%s", query)
		}
	})
	defer srv.Close()

	out, err := testClient(srv).ListChangeRequests(context.Background(), ChangeRequestQuery{
		Integration:    "github-1",
		Owner:          "acme",
		Repo:           "web",
		SourceBranches: []string{"fleet/eshu-7398"},
		HeadCommits:    []string{"deadbeef0001"},
	})
	if err != nil {
		t.Fatalf("ListChangeRequests: %v", err)
	}
	if len(out.ChangeRequests) != 2 {
		t.Fatalf("change requests = %d, want 2 (branch + commit)", len(out.ChangeRequests))
	}
	open := out.ChangeRequests[0]
	if open.GetSourceBranch() != "fleet/eshu-7398" || open.GetHeadCommit() != "abc123def456" {
		t.Errorf("branch,head = %q,%q, want the agent's branch and sha", open.GetSourceBranch(), open.GetHeadCommit())
	}
	if open.GetState() != integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN {
		t.Errorf("state = %v, want OPEN", open.GetState())
	}
	// Approved and green about the head commit: the verifier's MERGE gate.
	if open.GetReviewDecision() != integrationv1.ReviewDecision_REVIEW_DECISION_APPROVED {
		t.Errorf("review decision = %v, want APPROVED", open.GetReviewDecision())
	}
	if open.GetChecks().GetRollup() != integrationv1.CheckRollup_CHECK_ROLLUP_SUCCESS {
		t.Errorf("rollup = %v, want SUCCESS about the head commit", open.GetChecks().GetRollup())
	}
	// One CHANGES_REQUESTED followed by a new commit: one review round.
	if open.GetReviewRounds() != 1 {
		t.Errorf("review rounds = %d, want 1", open.GetReviewRounds())
	}
	if len(open.GetLinkedWorkItems()) != 1 || open.GetLinkedWorkItems()[0].GetKey().GetKey() != "7398" {
		t.Errorf("linked items = %v, want issue 7398", open.GetLinkedWorkItems())
	}
	if open.GetHeadRepo() != nil {
		t.Errorf("same-repo head sets no head_repo, got %v", open.GetHeadRepo())
	}

	merged := out.ChangeRequests[1]
	if merged.GetState() != integrationv1.WorkItemState_WORK_ITEM_STATE_MERGED {
		t.Errorf("state = %v, want MERGED", merged.GetState())
	}
	if merged.GetMergedBy().GetLogin() != "alice" {
		t.Errorf("merged_by = %v, want alice", merged.GetMergedBy())
	}
	if merged.GetHeadRepo().GetPath() != "fork/web" {
		t.Errorf("fork head_repo = %v, want fork/web", merged.GetHeadRepo())
	}
	if merged.GetChecks().GetRollup() != integrationv1.CheckRollup_CHECK_ROLLUP_NONE {
		t.Errorf("no-check rollup = %v, want NONE, never success", merged.GetChecks().GetRollup())
	}
	if out.Meta.GetCost() != 12 {
		t.Errorf("cost = %d, want the GraphQL points charged", out.Meta.GetCost())
	}
	if len(out.Meta.GetRateLimits()) != 1 || out.Meta.GetRateLimits()[0].GetRemaining() != 4988 {
		t.Errorf("rate limits = %v, want the GRAPHQL budget", out.Meta.GetRateLimits())
	}
}

func TestListChangeRequestsBatchLimit(t *testing.T) {
	t.Parallel()
	srv := graphQLServer(t, nil)
	defer srv.Close()

	branches := make([]string, 0, 51)
	for range 51 {
		branches = append(branches, "fleet/branch")
	}
	_, err := testClient(srv).ListChangeRequests(context.Background(), ChangeRequestQuery{
		Integration: "github-1", Owner: "acme", Repo: "web", SourceBranches: branches,
	})
	if err == nil {
		t.Fatal("51 branches succeeded, want UNSUPPORTED (max 50 per call)")
	}
}
