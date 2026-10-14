package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

func claimsServer(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "claims_page.json"))
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/repos/acme/web/issues/7398/comments"; r.URL.Path != want {
			t.Errorf("path = %s, want %s", r.URL.Path, want)
		}
		h := w.Header()
		h.Set("Content-Type", "application/json")
		h.Set("ETag", `W/"claims1"`)
		h.Set("X-RateLimit-Limit", "5000")
		h.Set("X-RateLimit-Remaining", "4990")
		h.Set("X-RateLimit-Reset", "1790859600")
		if r.Header.Get("If-None-Match") == `W/"claims1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_, _ = w.Write(body)
	}))
}

func TestListClaims(t *testing.T) {
	t.Parallel()
	srv := claimsServer(t)
	defer srv.Close()
	c := testClient(srv)
	c.ClaimMarker = "eshu-issue-driver claim"

	page, err := c.ListClaims(context.Background(), ClaimQuery{
		Integration: "github-1",
		Owner:       "acme",
		Repo:        "web",
		Number:      7398,
		FleetUsers:  map[string]bool{"fleetdev": true},
	})
	if err != nil {
		t.Fatalf("ListClaims: %v", err)
	}
	// Only comments carrying the claim marker count; chatter does not.
	if len(page.Claims) != 2 {
		t.Fatalf("claims = %d, want 2 (one fleet, one foreign)", len(page.Claims))
	}
	fleet := page.Claims[0]
	if !fleet.GetByFleet() || fleet.GetBy().GetLogin() != "fleetdev" {
		t.Errorf("first claim = %v, want the fleet claim", fleet)
	}
	if fleet.GetMechanism() != integrationv1.ClaimMechanism_CLAIM_MECHANISM_COMMENT {
		t.Errorf("mechanism = %v, want COMMENT", fleet.GetMechanism())
	}
	wantTime := time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC)
	if !fleet.GetClaimTime().AsTime().Equal(wantTime) {
		t.Errorf("claim time = %v, want %v", fleet.GetClaimTime().AsTime(), wantTime)
	}
	foreign := page.Claims[1]
	if foreign.GetByFleet() {
		t.Error("mallory's claim counts as foreign, not fleet")
	}
	if page.Validators.GetEtag() != `W/"claims1"` {
		t.Errorf("etag = %q, want the page validator", page.Validators.GetEtag())
	}

	again, err := c.ListClaims(context.Background(), ClaimQuery{
		Integration: "github-1", Owner: "acme", Repo: "web", Number: 7398,
		Validators: page.Validators, FleetUsers: map[string]bool{"fleetdev": true},
	})
	if err != nil {
		t.Fatalf("second ListClaims: %v", err)
	}
	if !again.NotModified || len(again.Claims) != 0 {
		t.Error("same ETag is an empty 304")
	}
}
