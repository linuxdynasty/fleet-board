package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/syncer"
)

func TestBridgeListsWorkItems(t *testing.T) {
	t.Parallel()
	srv := fixtureServer(t, "4999")
	defer srv.Close()
	a := &Adapter{C: testClient(srv), Integration: "github-1"}

	page, err := a.ListWorkItems(context.Background(), "acme/web", time.Time{}, nil, false)
	if err != nil {
		t.Fatalf("ListWorkItems: %v", err)
	}
	if len(page.Items) != 2 {
		t.Errorf("items = %d, want 2", len(page.Items))
	}
	if _, err := a.ListWorkItems(context.Background(), "acme", time.Time{}, nil, false); err == nil {
		t.Error("bare project succeeded, want UNSUPPORTED")
	}
}

func TestBridgeGetWorkItem(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/web/issues/7398" {
			t.Errorf("path = %s, want the single issue", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number": 7398, "title": "Fleet this",
			"user": {"login": "alice", "id": 1001}, "labels": [],
			"state": "open", "created_at": "2026-10-01T12:00:00Z",
			"updated_at": "2026-10-01T13:00:00Z", "body": "The text.", "assignees": []}`))
	}))
	defer srv.Close()

	detail, err := (&Adapter{C: testClient(srv), Integration: "github-1"}).
		GetWorkItem(context.Background(), "acme/web", "7398", nil)
	if err != nil {
		t.Fatalf("GetWorkItem: %v", err)
	}
	if detail.Body != "The text." {
		t.Errorf("body = %q, want the issue text for row 12", detail.Body)
	}
}

func TestBridgeConvertsErrors(t *testing.T) {
	t.Parallel()
	if err := convertError(nil); err != nil {
		t.Errorf("nil = %v, want nil", err)
	}
	var call *syncer.CallError
	if !errors.As(convertError(&Error{Provider: &integrationv1.ProviderError{
		Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_NOT_FOUND,
		Message: "nope",
	}}), &call) {
		t.Fatal("adapter error did not cross as CallError")
	}
	if call.Provider.GetKind() != integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_NOT_FOUND {
		t.Errorf("kind = %v, want NOT_FOUND", call.Provider.GetKind())
	}
}

func TestBridgeClaimsAndEvidence(t *testing.T) {
	t.Parallel()
	csrv := claimsServer(t)
	defer csrv.Close()
	a := &Adapter{C: testClient(csrv), Integration: "github-1", FleetUsers: map[string]bool{"fleetdev": true}}
	a.C.ClaimMarker = "eshu-issue-driver claim"

	claims, err := a.ListClaims(context.Background(), "acme/web", "7398", nil)
	if err != nil {
		t.Fatalf("ListClaims: %v", err)
	}
	if len(claims.Claims) != 2 || !claims.Claims[0].GetByFleet() {
		t.Errorf("claims = %v, want the fleet claim first", claims.Claims)
	}

	esrv := evidenceServer(t)
	defer esrv.Close()
	a.C = testClient(esrv)
	ev, _, err := a.FillEvidence(context.Background(), "acme/web", []int{7398}, []string{"agent-ready"})
	if err != nil {
		t.Fatalf("FillEvidence: %v", err)
	}
	if len(ev) != 1 || ev[0].Editor.GetLogin() != "bob" {
		t.Errorf("evidence = %+v, want bob's edit", ev)
	}
}
