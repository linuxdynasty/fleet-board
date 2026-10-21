package syncer

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

var (
	syncBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	syncNow  = syncBase.Add(2 * time.Hour)
)

type fakeAdapter struct {
	caps      *integrationv1.IntegrationCapabilities
	work      func(project string, since time.Time, validators *integrationv1.Validators, closed bool) (*WorkItemPage, error)
	claims    func(project, key string, validators *integrationv1.Validators) (*ClaimPage, error)
	changes   func(project string, branches, commits []string, linked string) (*ChangeRequestList, error)
	evidence  func(project string, numbers []int, labels []string) ([]Evidence, *integrationv1.ResponseMeta, error)
	workCalls atomic.Int32
}

func (f *fakeAdapter) Describe() *integrationv1.IntegrationCapabilities { return f.caps }

func (f *fakeAdapter) ListWorkItems(ctx context.Context, project string, since time.Time, validators *integrationv1.Validators, includeClosed bool) (*WorkItemPage, error) {
	f.workCalls.Add(1)
	return f.work(project, since, validators, includeClosed)
}

func (f *fakeAdapter) ListClaims(ctx context.Context, project, key string, validators *integrationv1.Validators) (*ClaimPage, error) {
	return f.claims(project, key, validators)
}

func (f *fakeAdapter) ListChangeRequests(ctx context.Context, project string, branches, commits []string, linked string) (*ChangeRequestList, error) {
	return f.changes(project, branches, commits, linked)
}

func (f *fakeAdapter) FillEvidence(ctx context.Context, project string, numbers []int, labels []string) ([]Evidence, *integrationv1.ResponseMeta, error) {
	return f.evidence(project, numbers, labels)
}

func testCaps() *integrationv1.IntegrationCapabilities {
	return &integrationv1.IntegrationCapabilities{
		Provider:        typev1.ProviderKind_PROVIDER_KIND_GITHUB,
		ProviderName:    "github",
		LabelsWithActor: true,
		EditEvidence:    true,
		MaxBatchSize:    50,
	}
}

func syncItem(key, login, id string, labels ...string) *integrationv1.WorkItem {
	var ls []*integrationv1.Label
	for _, name := range labels {
		ls = append(ls, &integrationv1.Label{Name: name})
	}
	return &integrationv1.WorkItem{
		Ref: &typev1.WorkItemRef{
			Key: &typev1.WorkItemKey{
				Integration: &typev1.IntegrationId{Value: "github-1"},
				Kind:        typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
				Project:     "acme/web",
				Key:         key,
			},
		},
		Title:      "item " + key,
		State:      integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN,
		Author:     &integrationv1.Person{Login: login, ProviderId: id},
		Labels:     ls,
		CreateTime: timestamppb.New(syncBase),
		UpdateTime: timestamppb.New(syncBase.Add(time.Hour)),
	}
}

func syncMeta() *integrationv1.ResponseMeta {
	return &integrationv1.ResponseMeta{
		RateLimits: []*integrationv1.RateLimitState{
			{
				Kind: integrationv1.RateLimitKind_RATE_LIMIT_KIND_CORE, Limit: 5000, Remaining: 4990,
				ResetTime: timestamppb.New(syncNow.Add(time.Hour)),
			},
		},
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir() + "/state"
	if err := statedb.EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "fleet-test", CACert: []byte("ca"), CAKey: []byte("key"),
		ServerCert: []byte("cert"), ServerKey: []byte("skey"),
		AccountSalt: make([]byte, 32), SaltVersion: 1, SettingsEpoch: "epoch",
		CreatedTime: syncBase,
	}
	if err := statedb.Create(context.Background(), dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s, err := statedb.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := statedb.EnsureIntegration(ctx, s, statedb.IntegrationRow{
		ID: "github-1", Provider: "GITHUB", BaseURL: "https://github.com", Label: "hub",
	}, syncNow.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := statedb.UpsertRepo(ctx, s, statedb.RepoRow{
		Host: "github.com", Path: "acme/web", CodeHost: "github-1", Enabled: true,
	}, syncNow.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return s
}

func testGate() *hubv1.TrustedGate {
	return &hubv1.TrustedGate{
		Users: []*hubv1.TrustedUser{
			{Integration: &typev1.IntegrationId{Value: "github-1"}, Login: "alice"},
		},
	}
}

func testSyncer(s *store.Store, a Adapter) (*Syncer, *[]TriageNotice) {
	var notices []TriageNotice
	sy := New(s, map[string]Adapter{"github-1": a}, testGate(),
		slog.New(slog.DiscardHandler), nil)
	sy.Clock = func() time.Time { return syncNow }
	sy.OnTriage = func(n TriageNotice) { notices = append(notices, n) }
	return sy, &notices
}

func emptyClaims() func(string, string, *integrationv1.Validators) (*ClaimPage, error) {
	return func(_ string, _ string, v *integrationv1.Validators) (*ClaimPage, error) {
		if v.GetEtag() != "" {
			return &ClaimPage{NotModified: true, Meta: syncMeta()}, nil
		}
		return &ClaimPage{Validators: &integrationv1.Validators{Etag: "c1"}, Meta: syncMeta()}, nil
	}
}

func noChanges() func(string, []string, []string, string) (*ChangeRequestList, error) {
	return func(string, []string, []string, string) (*ChangeRequestList, error) {
		return &ChangeRequestList{Meta: syncMeta()}, nil
	}
}

func noEvidence() func(string, []int, []string) ([]Evidence, *integrationv1.ResponseMeta, error) {
	return func(string, []int, []string) ([]Evidence, *integrationv1.ResponseMeta, error) {
		return nil, syncMeta(), nil
	}
}

func mirrorKey(key string) string {
	return "github-1/" + typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE.String() + "/acme/web/" + key
}

// A trusted and an untrusted item get the right stage in one round.
func TestPollOnceStagesTrustedAndUntrusted(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	a := &fakeAdapter{
		caps: testCaps(),
		work: func(string, time.Time, *integrationv1.Validators, bool) (*WorkItemPage, error) {
			return &WorkItemPage{
				Items: []*integrationv1.WorkItem{
					syncItem("1", "alice", "1001", "agent-ready"),
					syncItem("2", "bob", "1002"),
				},
				Validators: &integrationv1.Validators{Etag: "e1"},
				NextSince:  &integrationv1.ChangeCursor{UpdatedAfterTime: timestamppb.New(syncNow)},
				Meta:       syncMeta(),
			}, nil
		},
		claims:  emptyClaims(),
		changes: noChanges(),
		evidence: func(_ string, numbers []int, _ []string) ([]Evidence, *integrationv1.ResponseMeta, error) {
			var out []Evidence
			for _, n := range numbers {
				if n != 1 {
					continue
				}
				out = append(out, Evidence{Number: 1, Labels: []*integrationv1.Label{{
					Name:        "agent-ready",
					AppliedBy:   &integrationv1.Person{Login: "alice", ProviderId: "1001"},
					AppliedTime: timestamppb.New(syncBase.Add(30 * time.Minute)),
				}}})
			}
			return out, syncMeta(), nil
		},
	}
	sy, _ := testSyncer(s, a)
	if err := sy.PollOnce(context.Background(), "github-1", false); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	ctx := context.Background()
	trusted, err := statedb.GetWorkItemState(ctx, s, mirrorKey("1"))
	if err != nil {
		t.Fatal(err)
	}
	if trusted.Stage != int32(hubv1.WorkStage_WORK_STAGE_READY) {
		t.Errorf("trusted stage = %d, want READY", trusted.Stage)
	}
	if !strings.Contains(trusted.TrustJSON, `"trusted":true`) {
		t.Errorf("trusted verdict = %s, want trusted", trusted.TrustJSON)
	}
	untrusted, err := statedb.GetWorkItemState(ctx, s, mirrorKey("2"))
	if err != nil {
		t.Fatal(err)
	}
	if untrusted.Stage != int32(hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY) {
		t.Errorf("untrusted stage = %d, want MANUAL_ONLY", untrusted.Stage)
	}
	cursor, err := statedb.GetCursor(ctx, s, "github-1", ResourceWorkItems, "acme/web")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.ETag != "e1" || !cursor.HasSince {
		t.Errorf("cursor = %+v, want the page validators and cursor", cursor)
	}
	row, err := statedb.GetIntegration(ctx, s, "github-1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(row.Status), "INTEGRATION_HEALTH_OK") {
		t.Errorf("status = %s, want a clean round's OK", row.Status)
	}
}

// A 304 round changes nothing: versions hold, no Watch-only write.
func TestPollOnceNotModifiedKeepsVersions(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	a := &fakeAdapter{
		caps: testCaps(),
		work: func(_ string, _ time.Time, v *integrationv1.Validators, _ bool) (*WorkItemPage, error) {
			if v.GetEtag() != "" {
				return &WorkItemPage{NotModified: true, Meta: syncMeta()}, nil
			}
			return &WorkItemPage{
				Items:      []*integrationv1.WorkItem{syncItem("1", "alice", "1001")},
				Validators: &integrationv1.Validators{Etag: "e1"},
				NextSince:  &integrationv1.ChangeCursor{UpdatedAfterTime: timestamppb.New(syncNow)},
				Meta:       syncMeta(),
			}, nil
		},
		claims:   emptyClaims(),
		changes:  noChanges(),
		evidence: noEvidence(),
	}
	sy, _ := testSyncer(s, a)
	ctx := context.Background()
	if err := sy.PollOnce(ctx, "github-1", false); err != nil {
		t.Fatal(err)
	}
	versionOf := func() int64 {
		var v int64
		if err := s.Read(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `SELECT version FROM work_items WHERE key = ?`,
				mirrorKey("1")).Scan(&v)
		}); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if got := versionOf(); got != 2 {
		t.Fatalf("version = %d, want 2 (item plus the claim loop's write)", got)
	}
	if err := sy.PollOnce(ctx, "github-1", false); err != nil {
		t.Fatal(err)
	}
	if got := versionOf(); got != 2 {
		t.Errorf("version after 304 = %d, want 2 (nothing new, no bump)", got)
	}
	stored, err := statedb.GetWorkItemState(ctx, s, mirrorKey("1"))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Stage != int32(hubv1.WorkStage_WORK_STAGE_READY) {
		t.Errorf("stage after 304 = %d, want READY kept", stored.Stage)
	}
}

// An expired cursor refetches the full page once instead of failing the
// round (03j: cursors are hints, never required).
func TestCursorExpiredRefetchesFullPage(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	a := &fakeAdapter{
		caps: testCaps(),
		work: func(_ string, _ time.Time, v *integrationv1.Validators, _ bool) (*WorkItemPage, error) {
			if v.GetEtag() != "" {
				return nil, &CallError{Provider: &integrationv1.ProviderError{
					Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_CURSOR_EXPIRED,
					Message: "stale etag",
				}}
			}
			return &WorkItemPage{
				Items:      []*integrationv1.WorkItem{syncItem("1", "alice", "1001")},
				Validators: &integrationv1.Validators{Etag: "e2"},
				NextSince:  &integrationv1.ChangeCursor{UpdatedAfterTime: timestamppb.New(syncNow)},
				Meta:       syncMeta(),
			}, nil
		},
		claims:   emptyClaims(),
		changes:  noChanges(),
		evidence: noEvidence(),
	}
	sy, _ := testSyncer(s, a)
	ctx := context.Background()
	if err := sy.PollOnce(ctx, "github-1", false); err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if err := sy.PollOnce(ctx, "github-1", false); err != nil {
		t.Fatalf("expired cursor poll: %v", err)
	}
	cursor, err := statedb.GetCursor(ctx, s, "github-1", ResourceWorkItems, "acme/web")
	if err != nil {
		t.Fatal(err)
	}
	if cursor.ETag != "e2" {
		t.Errorf("cursor = %q, want the refetched validators", cursor.ETag)
	}
	if _, err := statedb.GetWorkItemState(ctx, s, mirrorKey("1")); err != nil {
		t.Errorf("mirror after refetch: %v", err)
	}
}

// An exhausted budget backs off to the reset and polls again after it.
func TestRateLimitedBacksOffToReset(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	now := syncNow
	a := &fakeAdapter{
		caps: testCaps(),
		work: func(string, time.Time, *integrationv1.Validators, bool) (*WorkItemPage, error) {
			return nil, &CallError{Provider: &integrationv1.ProviderError{
				Kind:       integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_RATE_LIMITED,
				Retryable:  true,
				RetryAfter: durationpb.New(time.Minute),
				RateLimit: &integrationv1.RateLimitState{
					Kind:  integrationv1.RateLimitKind_RATE_LIMIT_KIND_CORE,
					Limit: 5000, Remaining: 0, ResetTime: timestamppb.New(now.Add(time.Minute)),
				},
				Message: "rate limited",
			}}
		},
		claims:   emptyClaims(),
		changes:  noChanges(),
		evidence: noEvidence(),
	}
	sy, _ := testSyncer(s, a)
	sy.Clock = func() time.Time { return now }
	ctx := context.Background()
	if err := sy.PollOnce(ctx, "github-1", false); err == nil {
		t.Fatal("rate limit succeeded, want the error")
	}
	if got := a.workCalls.Load(); got != 1 {
		t.Fatalf("work calls = %d, want 1", got)
	}
	// Inside the back-off the round is skipped without a call.
	if err := sy.PollOnce(ctx, "github-1", false); err != nil {
		t.Fatalf("backed-off poll = %v, want a skip", err)
	}
	if got := a.workCalls.Load(); got != 1 {
		t.Errorf("work calls during back-off = %d, want still 1", got)
	}
	row, _ := statedb.GetIntegration(ctx, s, "github-1")
	if !strings.Contains(string(row.Status), "BACKING_OFF") {
		t.Errorf("status = %s, want BACKING_OFF", row.Status)
	}
	now = now.Add(2 * time.Minute)
	if err := sy.PollOnce(ctx, "github-1", false); err == nil {
		t.Fatal("third poll succeeded, want the error again after the reset")
	}
	if got := a.workCalls.Load(); got != 2 {
		t.Errorf("work calls after reset = %d, want 2", got)
	}
}

// A missing repo degrades the integration and raises access-lost; a clean
// round retires it.
func TestNotFoundRaisesAccessLost(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	fail := true
	a := &fakeAdapter{
		caps: testCaps(),
		work: func(string, time.Time, *integrationv1.Validators, bool) (*WorkItemPage, error) {
			if fail {
				return nil, &CallError{Provider: &integrationv1.ProviderError{
					Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_NOT_FOUND,
					Project: "acme/web", Message: "not found",
				}}
			}
			return &WorkItemPage{
				Items:      []*integrationv1.WorkItem{syncItem("1", "alice", "1001")},
				Validators: &integrationv1.Validators{Etag: "e1"},
				Meta:       syncMeta(),
			}, nil
		},
		claims:   emptyClaims(),
		changes:  noChanges(),
		evidence: noEvidence(),
	}
	sy, notices := testSyncer(s, a)
	ctx := context.Background()
	if err := sy.PollOnce(ctx, "github-1", false); err == nil {
		t.Fatal("missing repo succeeded, want NOT_FOUND")
	}
	if len(*notices) != 1 || (*notices)[0].Kind != "integration_access_lost" {
		t.Fatalf("notices = %+v, want one access-lost raise", *notices)
	}
	if (*notices)[0].Fingerprint != "integration_access_lost:github-1:acme/web" {
		t.Errorf("fingerprint = %q, want the 03d dedupe key", (*notices)[0].Fingerprint)
	}
	row, _ := statedb.GetIntegration(ctx, s, "github-1")
	if !strings.Contains(string(row.Status), "ACCESS_LOST") {
		t.Errorf("status = %s, want ACCESS_LOST", row.Status)
	}
	fail = false
	if err := sy.PollOnce(ctx, "github-1", false); err != nil {
		t.Fatalf("clean round: %v", err)
	}
	if len(*notices) != 2 || !(*notices)[1].Retire {
		t.Fatalf("notices = %+v, want a retire after the clean round", *notices)
	}
}
