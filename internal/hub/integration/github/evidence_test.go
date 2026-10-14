package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func evidenceServer(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "timeline_evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture any
	if err := json.Unmarshal(body, &fixture); err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(fixture)
	}))
}

func TestFillEvidence(t *testing.T) {
	t.Parallel()
	srv := evidenceServer(t)
	defer srv.Close()

	out, err := testClient(srv).FillEvidence(context.Background(), EvidenceQuery{
		Integration: "github-1",
		Owner:       "acme",
		Repo:        "web",
		Numbers:     []int{7398, 7399},
		Labels:      []string{"agent-ready"},
	})
	if err != nil {
		t.Fatalf("FillEvidence: %v", err)
	}
	if len(out.Items) != 2 {
		t.Fatalf("evidence items = %d, want 2", len(out.Items))
	}
	first := out.Items[0]
	if first.Number != 7398 {
		t.Errorf("number = %d, want 7398 in request order", first.Number)
	}
	// The editor and edit time come from the provider, not the mirror.
	if first.Editor.GetLogin() != "bob" || first.Editor.GetProviderId() != "1002" {
		t.Errorf("editor = %v, want bob/1002", first.Editor)
	}
	wantEdit := time.Date(2026, 10, 1, 13, 30, 0, 0, time.UTC)
	if !first.LastEditedAt.Equal(wantEdit) {
		t.Errorf("last edit = %v, want %v", first.LastEditedAt, wantEdit)
	}
	// The trusted label's latest apply decides the label path (03j).
	if len(first.Labels) != 1 {
		t.Fatalf("labels = %d, want only agent-ready (fleet was removed)", len(first.Labels))
	}
	got := first.Labels[0]
	if got.GetAppliedBy().GetLogin() != "alice" {
		t.Errorf("applied_by = %v, want alice", got.GetAppliedBy())
	}
	wantApply := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	if !got.GetAppliedTime().AsTime().Equal(wantApply) {
		t.Errorf("applied_time = %v, want %v", got.GetAppliedTime().AsTime(), wantApply)
	}

	second := out.Items[1]
	if second.Editor != nil || len(second.Labels) != 0 {
		t.Errorf("unknown evidence stays unknown, got %+v", second)
	}
	if out.Meta.GetCost() != 4 {
		t.Errorf("cost = %d, want 4", out.Meta.GetCost())
	}
}

func TestFillEvidenceEmpty(t *testing.T) {
	t.Parallel()
	srv := evidenceServer(t)
	defer srv.Close()

	out, err := testClient(srv).FillEvidence(context.Background(), EvidenceQuery{
		Integration: "github-1", Owner: "acme", Repo: "web",
	})
	if err != nil {
		t.Fatalf("empty FillEvidence: %v", err)
	}
	if len(out.Items) != 0 {
		t.Errorf("empty query fetched %d items, want none without a call", len(out.Items))
	}
}
