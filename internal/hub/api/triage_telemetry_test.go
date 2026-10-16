package api_test

import (
	"bufio"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// triageSample is the value of the triage_writes series with this op and
// outcome, or -1 when absent.
func triageSample(t *testing.T, body, op, outcome string) float64 {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "fleet_hub_triage_writes_total{") &&
			strings.Contains(line, `op="`+op+`"`) &&
			strings.Contains(line, `outcome="`+outcome+`"`) {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				return -1
			}
			return v
		}
	}
	return -1
}

// TestTriageWritesAreCountedByOpAndOutcome proves both hub-state triage
// writes are counted by op and outcome, and log their 3 AM events: a
// snooze, its replay, a refused validation and a dismiss.
func TestTriageWritesAreCountedByOpAndOutcome(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedTriageItem(t, e, "ti-1")
	seedTriageItem(t, e, "ti-2")
	until := timestamppb.New(fleetNow.Add(2 * time.Hour))
	snoozeCall(t, e, "", "sn-1", "ti-1", 1, until)
	snoozeCall(t, e, "", "sn-1", "ti-1", 1, until)
	if _, err := e.client.SnoozeTriageItem(t.Context(), withSecret(connect.NewRequest(&hubv1.SnoozeTriageItemRequest{
		RequestId: "sn-bad", Id: &hubv1.TriageItemId{Value: "ti-1"}, ExpectedVersion: 1,
	}), "")); err == nil {
		t.Fatal("missing until_time accepted")
	}
	if _, err := e.client.DismissTriageItem(t.Context(), withSecret(connect.NewRequest(&hubv1.DismissTriageItemRequest{
		RequestId: "di-1", Id: &hubv1.TriageItemId{Value: "ti-2"}, ExpectedVersion: 1,
	}), "")); err != nil {
		t.Fatalf("DismissTriageItem: %v", err)
	}
	body := e.scrape(t)
	cases := []struct {
		op, outcome string
		want        float64
	}{
		{"triage_snooze", "ok", 1},
		{"triage_snooze", "replayed", 1},
		{"triage_snooze", "refused", 1},
		{"triage_dismiss", "ok", 1},
	}
	for _, tc := range cases {
		if got := triageSample(t, body, tc.op, tc.outcome); got != tc.want {
			t.Errorf("triage_writes{%s %s} = %v, want %v", tc.op, tc.outcome, got, tc.want)
		}
	}
	if got := e.logs.events(t, "triage.snoozed"); len(got) != 2 {
		t.Errorf("triage.snoozed events = %d, want 2 (write and replay)", len(got))
	}
	if got := e.logs.events(t, "triage.dismissed"); len(got) != 1 {
		t.Errorf("triage.dismissed events = %d, want 1", len(got))
	}
	if got := e.logs.events(t, "triage.write_refused"); len(got) != 1 {
		t.Errorf("triage.write_refused events = %d, want 1", len(got))
	}
}
