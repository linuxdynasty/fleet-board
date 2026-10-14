package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

func fixtureServer(t *testing.T, remaining string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "issues_page1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer ghs_test" {
			t.Errorf("Authorization = %q, want the installation token", got)
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("ETag", `W/"abc123"`)
		h.Set("Last-Modified", "Wed, 01 Oct 2026 13:00:00 GMT")
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", remaining)
		h.Set("X-RateLimit-Reset", "1790859600")
		h.Set("Date", "Wed, 01 Oct 2026 13:00:01 GMT")
		if r.Header.Get("If-None-Match") == `W/"abc123"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(body)
	}))
}

func testClient(srv *httptest.Server) *Client {
	return &Client{
		HTTP:    srv.Client(),
		APIBase: srv.URL,
		Tokens:  TokenSourceFunc(func(context.Context) (string, error) { return "ghs_test", nil }),
	}
}

func TestListIssuesMapsPage(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t, "4999")
	defer srv.Close()

	page, err := testClient(srv).ListIssues(context.Background(), ListIssuesQuery{
		Integration: "github-1",
		Owner:       "acme",
		Repo:        "web",
	})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if page.NotModified {
		t.Fatal("first poll is not a 304")
	}
	// The pull-request entry filters out: mirrors track issues here,
	// change requests come from GraphQL.
	if len(page.Items) != 2 {
		t.Fatalf("items = %d, want 2 (one PR filtered)", len(page.Items))
	}
	first := page.Items[0]
	if first.GetRef().GetKey().GetKey() != "7398" {
		t.Errorf("key = %q, want 7398", first.GetRef().GetKey().GetKey())
	}
	if first.GetAuthor().GetLogin() != "alice" || first.GetAuthor().GetProviderId() != "1001" {
		t.Errorf("author = %v, want alice/1001", first.GetAuthor())
	}
	if len(first.GetLabels()) != 2 || first.GetLabels()[0].GetName() != "agent-ready" {
		t.Errorf("labels = %v, want the two names only", first.GetLabels())
	}
	if first.GetLabels()[0].GetAppliedBy() != nil {
		t.Error("list labels carry no actor: timeline evidence fills that")
	}
	if first.GetState() != integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN {
		t.Errorf("state = %v, want OPEN", first.GetState())
	}
	wantUpdate := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	if !first.GetUpdateTime().AsTime().Equal(wantUpdate) {
		t.Errorf("update_time = %v, want %v", first.GetUpdateTime().AsTime(), wantUpdate)
	}
	if page.Validators.GetEtag() != `W/"abc123"` {
		t.Errorf("etag = %q, want the response validator", page.Validators.GetEtag())
	}
	if len(page.Meta.GetRateLimits()) != 1 || page.Meta.GetRateLimits()[0].GetRemaining() != 4999 {
		t.Errorf("rate limits = %v, want the reported budget", page.Meta.GetRateLimits())
	}
	closed := page.Items[1]
	if closed.GetState() != integrationv1.WorkItemState_WORK_ITEM_STATE_CLOSED {
		t.Errorf("second item state = %v, want CLOSED", closed.GetState())
	}
}

func TestListIssuesNotModifiedIsFree(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t, "4999")
	defer srv.Close()
	c := testClient(srv)
	ctx := context.Background()

	first, err := c.ListIssues(ctx, ListIssuesQuery{Integration: "github-1", Owner: "acme", Repo: "web"})
	if err != nil {
		t.Fatalf("first ListIssues: %v", err)
	}
	before := first.Meta.GetRateLimits()[0].GetRemaining()
	second, err := c.ListIssues(ctx, ListIssuesQuery{
		Integration: "github-1", Owner: "acme", Repo: "web",
		Validators: first.Validators,
	})
	if err != nil {
		t.Fatalf("second ListIssues: %v", err)
	}
	if !second.NotModified {
		t.Fatal("same ETag is a 304")
	}
	if len(second.Items) != 0 {
		t.Errorf("304 carries %d items, want none", len(second.Items))
	}
	// 04 Q8: an authorized 304 does not count against the primary limit.
	if got := second.Meta.GetRateLimits()[0].GetRemaining(); got != before {
		t.Errorf("remaining after 304 = %d, want %d (free)", got, before)
	}
}

func TestGetIssueReadsBody(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/repos/acme/web/issues/7398"; r.URL.Path != want {
			t.Errorf("path = %s, want %s", r.URL.Path, want)
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("ETag", `W/"item1"`)
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", "4998")
		h.Set("X-RateLimit-Reset", "1790859600")
		if r.Header.Get("If-None-Match") == `W/"item1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write([]byte(`{"number": 7398, "title": "Fleet this",
			"user": {"login": "alice", "id": 1001}, "labels": [{"name": "agent-ready"}],
			"state": "open", "created_at": "2026-10-01T12:00:00Z",
			"updated_at": "2026-10-01T13:00:00Z", "body": "The full text.", "assignees": []}`))
	}))
	defer srv.Close()

	detail, err := testClient(srv).GetIssue(context.Background(), "github-1", "acme", "web", 7398, nil)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if detail.Item.GetTitle() != "Fleet this" || detail.Body != "The full text." {
		t.Errorf("item = %+v body %q, want the issue and its text", detail.Item, detail.Body)
	}
	again, err := testClient(srv).GetIssue(context.Background(), "github-1", "acme", "web", 7398, detail.Validators)
	if err != nil {
		t.Fatalf("second GetIssue: %v", err)
	}
	if !again.NotModified || again.Item != nil {
		t.Error("same ETag is an empty 304")
	}
}

func TestListIssuesNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message": "Not Found"}`))
	}))
	defer srv.Close()

	_, err := testClient(srv).ListIssues(context.Background(), ListIssuesQuery{
		Integration: "github-1", Owner: "acme", Repo: "missing",
	})
	if err == nil {
		t.Fatal("404 succeeded, want a ProviderError")
	}
	var callErr *Error
	if !errors.As(err, &callErr) {
		t.Fatalf("error is %T, want *Error", err)
	}
	if callErr.Provider.GetKind() != integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_NOT_FOUND {
		t.Errorf("kind = %v, want NOT_FOUND", callErr.Provider.GetKind())
	}
	if callErr.Provider.GetRetryable() {
		t.Error("NOT_FOUND is not retryable")
	}
}
