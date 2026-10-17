package api_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

var goalBase = time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC)

// observedGoalSpec builds the immutable spec an observed goal stores (03k):
// machine, first line and origin only.
func observedGoalSpec(machine, text string) string {
	out, err := protojson.Marshal(&hubv1.GoalDefinition{
		Machine:  &typev1.MachineId{Value: machine},
		GoalText: text,
		Origin:   hubv1.GoalOrigin_GOAL_ORIGIN_OBSERVED,
	})
	if err != nil {
		panic(err)
	}
	return string(out)
}

// observedGoalStatus builds the status an observed goal stores.
func observedGoalStatus(phase hubv1.GoalPhase, reason hubv1.GoalPhaseReason, session string, start time.Time) string {
	st := &hubv1.GoalStatus{
		Phase:       phase,
		PhaseReason: reason,
		PhaseTime:   timestamppb.New(start),
		LineState:   typev1.GoalLineState_GOAL_LINE_STATE_WORKING,
		FirstLine:   "work on it",
		StartTime:   timestamppb.New(start),
	}
	if session != "" {
		st.Session = &typev1.SessionId{Value: session}
	}
	if phase == hubv1.GoalPhase_GOAL_PHASE_CLOSED {
		st.EndTime = timestamppb.New(start)
	}
	out, err := protojson.Marshal(st)
	if err != nil {
		panic(err)
	}
	return string(out)
}

// writeObservedGoal stores one observed goal row for List/Get tests.
func writeObservedGoal(t *testing.T, e *fleetEnv, id, machine string, phase hubv1.GoalPhase,
	session string, start time.Time,
) {
	t.Helper()
	reason := hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED
	status := observedGoalStatus(phase, reason, session, start)
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		return statedb.UpsertObservedGoal(context.Background(), tx, statedb.ObservedRow{
			ID: id, MachineID: machine, Spec: observedGoalSpec(machine, "work on it"),
			Status: status, Phase: phase.String(), Reason: reason.String(),
			StartMs: start.UnixMilli(), ObservedMs: start.UnixMilli(), HubSeq: 1,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func listGoals(t *testing.T, e *fleetEnv, req *hubv1.ListGoalsRequest) *hubv1.ListGoalsResponse {
	t.Helper()
	resp, err := e.client.ListGoals(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	return resp.Msg
}

// TestListGoalsServesObservedGoals is 02k's observed-goal read: every
// hand-started goal with its stored spec and status, in start-time order.
func TestListGoalsServesObservedGoals(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-beta", "m-two", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase)
	writeObservedGoal(t, e, "obs-alpha", "m-one", hubv1.GoalPhase_GOAL_PHASE_WAITING, "sess-1", goalBase.Add(-time.Hour))

	msg := listGoals(t, e, &hubv1.ListGoalsRequest{})
	if len(msg.GetGoals()) != 2 {
		t.Fatalf("goals %d, want 2 observed", len(msg.GetGoals()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	first, second := msg.GetGoals()[0], msg.GetGoals()[1]
	if first.GetId().GetValue() != "obs-alpha" || second.GetId().GetValue() != "obs-beta" {
		t.Errorf("order %q, %q, want obs-alpha then obs-beta (default order is start_time)",
			first.GetId().GetValue(), second.GetId().GetValue())
	}
	if first.GetVersion() != 1 {
		t.Errorf("version %d, want 1 (the observed spec never changes)", first.GetVersion())
	}
	if !first.GetCreateTime().IsValid() || !first.GetUpdateTime().IsValid() {
		t.Error("create_time and update_time are unset, want the start and observed times")
	}
	if got := first.GetSpec().GetMachine().GetValue(); got != "m-one" {
		t.Errorf("spec.machine %q, want m-one", got)
	}
	if first.GetSpec().GetOrigin() != hubv1.GoalOrigin_GOAL_ORIGIN_OBSERVED {
		t.Errorf("spec.origin %s, want OBSERVED", first.GetSpec().GetOrigin())
	}
	if first.GetSpec().GetGoalText() != "work on it" {
		t.Errorf("spec.goal_text %q, want the goal file's first line", first.GetSpec().GetGoalText())
	}
	if first.GetStatus().GetPhase() != hubv1.GoalPhase_GOAL_PHASE_WAITING {
		t.Errorf("status.phase %s, want WAITING", first.GetStatus().GetPhase())
	}
	if got := first.GetStatus().GetSession().GetValue(); got != "sess-1" {
		t.Errorf("status.session %q, want sess-1", got)
	}
	if second.GetStatus().GetSession() != nil {
		t.Errorf("status.session %v, want unset (no pane session reported)", second.GetStatus().GetSession())
	}
}

// TestListGoalsFilters covers the served half of the 02c Goal catalogue:
// phase, machine, origin, live, session, phase_reason, id and the times.
func TestListGoalsFilters(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-alpha", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "sess-1", goalBase.Add(-time.Hour))
	writeObservedGoal(t, e, "obs-beta", "m-two", hubv1.GoalPhase_GOAL_PHASE_WAITING, "", goalBase)

	for _, tc := range []struct {
		name   string
		filter string
		want   []string
	}{
		{"phase equals", `phase = RUNNING`, []string{"obs-alpha"}},
		{"phase not-equals", `phase != RUNNING`, []string{"obs-beta"}},
		{"phase in", `phase in (RUNNING, WAITING)`, []string{"obs-alpha", "obs-beta"}},
		{"closed matches nothing yet", `phase = CLOSED`, nil},
		{"machine equals", `machine = "m-one"`, []string{"obs-alpha"}},
		{"machine in", `machine in ("m-one", "m-two")`, []string{"obs-alpha", "obs-beta"}},
		{"origin observed", `origin = OBSERVED`, []string{"obs-alpha", "obs-beta"}},
		{"origin dispatched matches nothing", `origin = DISPATCHED`, nil},
		{"live matches open goals", `live = true`, []string{"obs-alpha", "obs-beta"}},
		{"not live matches nothing open", `live = false`, nil},
		{"a dispatched-only phase parses but matches nothing", `phase = STARTING`, nil},
		{"session equals", `session = "sess-1"`, []string{"obs-alpha"}},
		{"phase reason", `phase_reason = UNSPECIFIED`, []string{"obs-alpha", "obs-beta"}},
		{"started after", `start_time > "2026-10-06T03:30:00Z"`, []string{"obs-beta"}},
		{"started before or at", `start_time <= "2026-10-06T03:00:00Z"`, []string{"obs-alpha"}},
		{"created is first sight", `create_time = "2026-10-06T03:00:00Z"`, []string{"obs-alpha"}},
		{"updated is observed time", `update_time = "2026-10-06T04:00:00Z"`, []string{"obs-beta"}},
		{"no filter returns both", ``, []string{"obs-alpha", "obs-beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listGoals(t, e, &hubv1.ListGoalsRequest{Filter: tc.filter})
			var got []string
			for _, g := range msg.GetGoals() {
				got = append(got, g.GetId().GetValue())
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ids %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("ids %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestListGoalsClosedGoal covers the end of an observed goal: DONE closes
// it with OBSERVED_DONE and stamps the end, so it reads CLOSED, not live,
// with an end_time the timeline can filter on.
func TestListGoalsClosedGoal(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-open", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase.Add(-time.Hour))
	end := goalBase.UnixMilli()
	closedStatus := observedGoalStatus(hubv1.GoalPhase_GOAL_PHASE_CLOSED,
		hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_DONE, "", goalBase)
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		return statedb.UpsertObservedGoal(context.Background(), tx, statedb.ObservedRow{
			ID: "obs-done", MachineID: "m-one", Spec: observedGoalSpec("m-one", "done work"),
			Status: closedStatus, Phase: hubv1.GoalPhase_GOAL_PHASE_CLOSED.String(),
			Reason:  hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_DONE.String(),
			StartMs: goalBase.Add(-2 * time.Hour).UnixMilli(), ObservedMs: goalBase.UnixMilli(), HubSeq: 2,
			EndMs: &end,
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	msg := listGoals(t, e, &hubv1.ListGoalsRequest{Filter: `phase = CLOSED`})
	if len(msg.GetGoals()) != 1 || msg.GetGoals()[0].GetId().GetValue() != "obs-done" {
		t.Fatalf("closed goals %v, want [obs-done]", msg.GetGoals())
	}
	if got := msg.GetGoals()[0].GetStatus().GetEndTime(); !got.IsValid() {
		t.Error("end_time is unset, want the close time")
	}
	msg = listGoals(t, e, &hubv1.ListGoalsRequest{Filter: `live = false`})
	if len(msg.GetGoals()) != 1 || msg.GetGoals()[0].GetId().GetValue() != "obs-done" {
		t.Fatalf("not-live goals %v, want [obs-done]", msg.GetGoals())
	}
	msg = listGoals(t, e, &hubv1.ListGoalsRequest{Filter: `end_time > "2026-10-06T03:00:00Z"`})
	if len(msg.GetGoals()) != 1 || msg.GetGoals()[0].GetId().GetValue() != "obs-done" {
		t.Fatalf("ended-after goals %v, want [obs-done]", msg.GetGoals())
	}
}

// TestGetGoalAfterSnapshotDrop covers the OBSERVED_GONE path end to end:
// a snapshot that no longer lists the goal ends it in the row columns
// only, and the read still reports CLOSED with its end_time, matching
// what the filter sees.
func TestGetGoalAfterSnapshotDrop(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-gone", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase)
	dropMs := goalBase.Add(time.Hour).UnixMilli()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := statedb.EndObservedGone(context.Background(), tx, "m-one", nil, dropMs, 2)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := e.client.GetGoal(context.Background(), connect.NewRequest(&hubv1.GetGoalRequest{
		Id: &typev1.GoalId{Value: "obs-gone"},
	}))
	if err != nil {
		t.Fatalf("GetGoal: %v", err)
	}
	got := resp.Msg.GetGoal()
	if got.GetStatus().GetPhase() != hubv1.GoalPhase_GOAL_PHASE_CLOSED {
		t.Errorf("phase %s, want CLOSED after the snapshot drop", got.GetStatus().GetPhase())
	}
	if got.GetStatus().GetPhaseReason() != hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_GONE {
		t.Errorf("reason %s, want OBSERVED_GONE", got.GetStatus().GetPhaseReason())
	}
	if end := got.GetStatus().GetEndTime(); !end.IsValid() ||
		end.AsTime().UnixMilli() != dropMs {
		t.Errorf("end_time %v, want the drop time", end)
	}
	msg := listGoals(t, e, &hubv1.ListGoalsRequest{Filter: `phase = CLOSED`})
	if len(msg.GetGoals()) != 1 || msg.GetGoals()[0].GetStatus().GetPhase() != hubv1.GoalPhase_GOAL_PHASE_CLOSED {
		t.Fatalf("closed goals %v, want [obs-gone] reading CLOSED", msg.GetGoals())
	}
	msg = listGoals(t, e, &hubv1.ListGoalsRequest{Filter: `live = false`})
	if len(msg.GetGoals()) != 1 {
		t.Fatalf("not-live goals %d, want 1 (the dropped goal)", len(msg.GetGoals()))
	}
}

// TestListGoalsRefusesDispatchFilters is the unserved half of the 02c Goal
// catalogue: dispatch-only fields stay FILTER_INVALID until their rows land.
func TestListGoalsRefusesDispatchFilters(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	for _, filter := range []string{
		`account = "acc-1"`, `repo = "h/r"`, `routine = "r1"`, `work_item = "k"`, `attempt = 1`,
	} {
		_, err := e.client.ListGoals(context.Background(), connect.NewRequest(&hubv1.ListGoalsRequest{Filter: filter}))
		if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
			reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
			t.Errorf("filter %q: got %v %v, want invalid_argument FILTER_INVALID", filter, code, reason)
		}
	}
}

// TestListGoalsPages walks two pages with a query-bound token, then rejects
// a token minted for another query.
func TestListGoalsPages(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-a", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase.Add(-2*time.Hour))
	writeObservedGoal(t, e, "obs-b", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase.Add(-time.Hour))
	writeObservedGoal(t, e, "obs-c", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase)

	first := listGoals(t, e, &hubv1.ListGoalsRequest{PageSize: 2})
	if len(first.GetGoals()) != 2 || first.GetNextPageToken() == "" {
		t.Fatalf("first page holds %d goals token %q, want 2 and a token",
			len(first.GetGoals()), first.GetNextPageToken())
	}
	second := listGoals(t, e, &hubv1.ListGoalsRequest{PageSize: 2, PageToken: first.GetNextPageToken()})
	if len(second.GetGoals()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("second page holds %d goals token %q, want 1 and no token",
			len(second.GetGoals()), second.GetNextPageToken())
	}
	if got := second.GetGoals()[0].GetId().GetValue(); got != "obs-c" {
		t.Errorf("second page id %q, want obs-c", got)
	}
	other := listGoals(t, e, &hubv1.ListGoalsRequest{PageSize: 1})
	_, err := e.client.ListGoals(context.Background(), connect.NewRequest(&hubv1.ListGoalsRequest{
		PageSize: 2, PageToken: other.GetNextPageToken(),
	}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: got %v %v, want invalid_argument PAGE_TOKEN_INVALID", code, reason)
	}
}

// TestGetGoalRoundTrip reads one observed goal back, and misses an unknown
// id with RESOURCE_NOT_FOUND.
func TestGetGoalRoundTrip(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-alpha", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase)

	resp, err := e.client.GetGoal(context.Background(), connect.NewRequest(&hubv1.GetGoalRequest{
		Id: &typev1.GoalId{Value: "obs-alpha"},
	}))
	if err != nil {
		t.Fatalf("GetGoal: %v", err)
	}
	if got := resp.Msg.GetGoal().GetSpec().GetGoalText(); got != "work on it" {
		t.Errorf("goal_text %q, want the stored first line", got)
	}
	_, err = e.client.GetGoal(context.Background(), connect.NewRequest(&hubv1.GetGoalRequest{
		Id: &typev1.GoalId{Value: "obs-missing"},
	}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown id: got %v %v, want not_found RESOURCE_NOT_FOUND", code, reason)
	}
	_, err = e.client.GetGoal(context.Background(), connect.NewRequest(&hubv1.GetGoalRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Errorf("empty id: got %v %v, want invalid_argument INVALID_ARGUMENT", code, reason)
	}
}

// TestGoalReadsDeniedPaths is the 02d READ gate: only the local user calls
// until row 12 brings caller scopes.
func TestGoalReadsDeniedPaths(t *testing.T) {
	t.Parallel()
	user := &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	}
	for _, tc := range []struct {
		name   string
		who    *typev1.Principal
		code   connect.Code
		reason typev1.ReasonCode
	}{
		{"no principal", nil, connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID},
		{"a signed-in user, before row 12 brings scopes", user, connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_SCOPE_MISSING},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newFleetEnv(t, tc.who)
			_, err := e.client.ListGoals(context.Background(), connect.NewRequest(&hubv1.ListGoalsRequest{}))
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Errorf("ListGoals: got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
			_, err = e.client.GetGoal(context.Background(), connect.NewRequest(&hubv1.GetGoalRequest{
				Id: &typev1.GoalId{Value: "obs-x"},
			}))
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Errorf("GetGoal: got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
}
