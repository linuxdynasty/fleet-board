package syncer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// A foreign claim parks the item; a fleet claim against an earlier foreign
// claim raises double_claim for a person.
func TestClaimLoopParksForeignClaims(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	claims := []*integrationv1.Claim{}
	a := &fakeAdapter{
		caps: testCaps(),
		work: func(string, time.Time, *integrationv1.Validators, bool) (*WorkItemPage, error) {
			return &WorkItemPage{
				Items:      []*integrationv1.WorkItem{syncItem("1", "alice", "1001")},
				Validators: &integrationv1.Validators{Etag: "e1"},
				Meta:       syncMeta(),
			}, nil
		},
		claims: func(string, string, *integrationv1.Validators) (*ClaimPage, error) {
			return &ClaimPage{Claims: claims, Validators: &integrationv1.Validators{Etag: "c1"}, Meta: syncMeta()}, nil
		},
		changes:  noChanges(),
		evidence: noEvidence(),
	}
	sy, notices := testSyncer(s, a)
	sy.FleetUsers = map[string]bool{"fleetdev": true}
	ctx := context.Background()
	if err := sy.PollOnce(ctx, "github-1", false); err != nil {
		t.Fatal(err)
	}
	claims = []*integrationv1.Claim{
		{
			Mechanism: integrationv1.ClaimMechanism_CLAIM_MECHANISM_COMMENT,
			By:        &integrationv1.Person{Login: "mallory"}, ClaimTime: timestamppb.New(syncNow), ByFleet: false,
		},
	}
	if err := sy.PollOnce(ctx, "github-1", false); err != nil {
		t.Fatal(err)
	}
	stored, _ := statedb.GetWorkItemState(ctx, s, mirrorKey("1"))
	if stored.Stage != int32(hubv1.WorkStage_WORK_STAGE_CLAIMED_ELSEWHERE) {
		t.Errorf("stage with foreign claim = %d, want CLAIMED_ELSEWHERE", stored.Stage)
	}
	claims = []*integrationv1.Claim{
		{
			Mechanism: integrationv1.ClaimMechanism_CLAIM_MECHANISM_COMMENT,
			By:        &integrationv1.Person{Login: "fleetdev"}, ClaimTime: timestamppb.New(syncNow), ByFleet: true,
		},
		{
			Mechanism: integrationv1.ClaimMechanism_CLAIM_MECHANISM_COMMENT,
			By:        &integrationv1.Person{Login: "mallory"}, ClaimTime: timestamppb.New(syncNow.Add(-time.Hour)), ByFleet: false,
		},
	}
	if err := sy.PollOnce(ctx, "github-1", false); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range *notices {
		if n.Kind == "double_claim" {
			found = true
		}
	}
	if !found {
		t.Errorf("notices = %+v, want double_claim for the earlier foreign claim", *notices)
	}
}

// Every request is counted with its result through the production meter.
func TestMetricsCountRequests(t *testing.T) {
	t.Parallel()
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Hub, InstanceID: "test", LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tel.Shutdown(context.Background()) }()
	metrics, err := NewMetrics(tel)
	if err != nil {
		t.Fatal(err)
	}
	s := testStore(t)
	a := &fakeAdapter{
		caps: testCaps(),
		work: func(string, time.Time, *integrationv1.Validators, bool) (*WorkItemPage, error) {
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
	sy, _ := testSyncer(s, a)
	sy.Metrics = metrics
	if err := sy.PollOnce(context.Background(), "github-1", false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(tel.MetricsHandler(nil))
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	req.RemoteAddr = "127.0.0.1:1"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	for _, want := range []string{"fleet_hub_sync_requests_total", "fleet_hub_sync_lag_seconds", "fleet_hub_integration_state"} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics miss %s", want)
		}
	}
}
