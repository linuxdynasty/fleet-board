package api_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

var usageBase = time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)

// insertUsageRow stores one reading for the List tests: the account, the
// hub-adjusted read time, the hub receive time and the source.
func insertUsageRow(t *testing.T, e *fleetEnv, account string, at time.Time, source string) {
	t.Helper()
	if _, err := statedb.InsertUsageReading(t.Context(), e.st, statedb.UsageSeed{
		AccountID: account, ReadTime: at, ReceivedTime: at.Add(time.Minute),
		AgentReadTime: at, Source: source, Parse: "OK",
		BucketsJSON: []byte(`[{"key":"weekly_all"}]`),
	}); err != nil {
		t.Fatalf("InsertUsageReading %s: %v", account, err)
	}
}

func seedUsageTrio(t *testing.T, e *fleetEnv) (late, mid, early string) {
	t.Helper()
	late = "01kusage000000000000000001"
	mid = "01kusage000000000000000002"
	early = late
	insertUsageRow(t, e, late, usageBase, "PROBE")
	insertUsageRow(t, e, mid, usageBase.Add(-time.Hour), "PASSIVE")
	insertUsageRow(t, e, early, usageBase.Add(-2*time.Hour), "PROBE")
	return late, mid, early
}

func listUsage(t *testing.T, e *fleetEnv, req *hubv1.ListUsageReadingsRequest) *hubv1.ListUsageReadingsResponse {
	t.Helper()
	resp, err := e.client.ListUsageReadings(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListUsageReadings: %v", err)
	}
	return resp.Msg
}

func usageAccounts(msg *hubv1.ListUsageReadingsResponse) []string {
	var out []string
	for _, r := range msg.GetReadings() {
		out = append(out, r.GetAccount().GetValue())
	}
	return out
}

// TestListUsageReadingsServesStoredReadings is 02k's usage read: every
// stored reading with its account, times, source and buckets, newest
// first by default.
func TestListUsageReadingsServesStoredReadings(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	late, _, _ := seedUsageTrio(t, e)

	msg := listUsage(t, e, &hubv1.ListUsageReadingsRequest{})
	if len(msg.GetReadings()) != 3 {
		t.Fatalf("readings %d, want 3 stored readings", len(msg.GetReadings()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	first := msg.GetReadings()[0]
	if first.GetAccount().GetValue() != late {
		t.Errorf("first account %q, want the newest reading of %q", first.GetAccount().GetValue(), late)
	}
	if !first.GetReadTime().IsValid() || !first.GetReceivedTime().IsValid() {
		t.Error("read_time and received_time unset, want the stored times")
	}
	if first.GetSource() != typev1.UsageSource_USAGE_SOURCE_PROBE {
		t.Errorf("source %s, want PROBE", first.GetSource())
	}
	if first.GetParse() != typev1.ParseOutcome_PARSE_OUTCOME_OK {
		t.Errorf("parse %s, want OK", first.GetParse())
	}
	if len(first.GetBuckets()) != 1 || first.GetBuckets()[0].GetKey() != "weekly_all" {
		t.Errorf("buckets %+v, want the stored weekly_all bucket", first.GetBuckets())
	}
	last := msg.GetReadings()[2]
	if last.GetAccount().GetValue() != late || last.GetSource() != typev1.UsageSource_USAGE_SOURCE_PROBE {
		t.Errorf("last %+v, want the oldest PROBE reading of %q", last, late)
	}
	if got := last.GetReadTime().AsTime(); !got.Equal(usageBase.Add(-2 * time.Hour)) {
		t.Errorf("oldest read_time %v, want base minus 2h", got)
	}
}

// TestListUsageReadingsFiltersPagesAndOrders covers the 02c
// UsageReading catalogue with the typed account: scoping, every served
// field, ordering and paging.
func TestListUsageReadingsFiltersPagesAndOrders(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	_, mid, _ := seedUsageTrio(t, e)

	byAccount := listUsage(t, e, &hubv1.ListUsageReadingsRequest{
		Account: &typev1.AccountId{Value: mid},
	})
	if len(byAccount.GetReadings()) != 1 || byAccount.GetReadings()[0].GetAccount().GetValue() != mid {
		t.Errorf("account scope matched %v, want the one reading of %q", usageAccounts(byAccount), mid)
	}

	cases := []struct {
		name   string
		filter string
		wantN  int
	}{
		{"source", `source = PROBE`, 2},
		{"source in", `source in (PROBE, PASSIVE)`, 3},
		{"source misses", `source = IDENTITY_PROBE`, 0},
		{"read time", `read_time > "2026-10-08T06:30:00Z"`, 1},
		{"read time range", `read_time <= "2026-10-08T07:00:00Z"`, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listUsage(t, e, &hubv1.ListUsageReadingsRequest{Filter: tc.filter})
			if len(msg.GetReadings()) != tc.wantN {
				t.Errorf("filter %q matched %d, want %d", tc.filter, len(msg.GetReadings()), tc.wantN)
			}
		})
	}

	asc := listUsage(t, e, &hubv1.ListUsageReadingsRequest{OrderBy: "read_time"})
	if len(asc.GetReadings()) != 3 {
		t.Fatalf("order_by read_time holds %d, want 3", len(asc.GetReadings()))
	}
	times := []time.Time{
		asc.GetReadings()[0].GetReadTime().AsTime(),
		asc.GetReadings()[1].GetReadTime().AsTime(),
		asc.GetReadings()[2].GetReadTime().AsTime(),
	}
	if !times[0].Equal(usageBase.Add(-2*time.Hour)) || !times[1].Equal(usageBase.Add(-time.Hour)) || !times[2].Equal(usageBase) {
		t.Errorf("order_by read_time times %v, want oldest first ending newest", times)
	}

	first := listUsage(t, e, &hubv1.ListUsageReadingsRequest{PageSize: 2})
	if len(first.GetReadings()) != 2 || first.GetNextPageToken() == "" {
		t.Fatalf("page 1 holds %d readings with token %q, want 2 readings and a token",
			len(first.GetReadings()), first.GetNextPageToken())
	}
	second := listUsage(t, e, &hubv1.ListUsageReadingsRequest{PageSize: 2, PageToken: first.GetNextPageToken()})
	if len(second.GetReadings()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("page 2 holds %d readings with token %q, want the last reading and no token",
			len(second.GetReadings()), second.GetNextPageToken())
	}

	_, err := e.client.ListUsageReadings(context.Background(), connect.NewRequest(
		&hubv1.ListUsageReadingsRequest{PageSize: 2, PageToken: first.GetNextPageToken(), Filter: `source = PROBE`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: %v %v, want INVALID_ARGUMENT PAGE_TOKEN_INVALID", code, reason)
	}

	_, err = e.client.ListUsageReadings(context.Background(), connect.NewRequest(
		&hubv1.ListUsageReadingsRequest{Filter: `id = "x"`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
		t.Errorf("id filter: %v %v, want INVALID_ARGUMENT FILTER_INVALID (readings have no id)", code, reason)
	}
}

// TestListUsageReadingsRefusesBadCallers covers the caller and account
// checks: no caller, a signed-in user without READ, and an empty
// account value.
func TestListUsageReadingsRefusesBadCallers(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedUsageTrio(t, e)

	anon := newFleetEnv(t, nil)
	_, err := anon.client.ListUsageReadings(context.Background(), connect.NewRequest(&hubv1.ListUsageReadingsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}

	user := newFleetEnv(t, &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	})
	_, err = user.client.ListUsageReadings(context.Background(), connect.NewRequest(&hubv1.ListUsageReadingsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}

	_, err = e.client.ListUsageReadings(context.Background(), connect.NewRequest(
		&hubv1.ListUsageReadingsRequest{Account: &typev1.AccountId{}}))
	if code, _, _ := detail(t, err); code != connect.CodeInvalidArgument {
		t.Errorf("empty account: %v, want INVALID_ARGUMENT", code)
	}
}
