package api_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// writeClosedObservedGoal stores one observed goal the hub already ended,
// so its run carries the ending.
func writeClosedObservedGoal(t *testing.T, e *fleetEnv, id, machine string, start time.Time) {
	t.Helper()
	end := start.Add(time.Hour).UnixMilli()
	st := observedGoalStatus(hubv1.GoalPhase_GOAL_PHASE_CLOSED,
		hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_DONE, "sess-9", start)
	var decoded hubv1.GoalStatus
	if err := protojson.Unmarshal([]byte(st), &decoded); err != nil {
		t.Fatal(err)
	}
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		return statedb.UpsertObservedGoal(context.Background(), tx, statedb.ObservedRow{
			ID: id, MachineID: machine, Spec: observedGoalSpec(machine, "done work"),
			Status: st, Phase: hubv1.GoalPhase_GOAL_PHASE_CLOSED.String(),
			Reason:  hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_DONE.String(),
			StartMs: start.UnixMilli(), ObservedMs: start.Add(time.Hour).UnixMilli(), HubSeq: 2,
			EndMs: &end,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func listGoalRuns(t *testing.T, e *fleetEnv, req *hubv1.ListGoalRunsRequest) *hubv1.ListGoalRunsResponse {
	t.Helper()
	resp, err := e.client.ListGoalRuns(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListGoalRuns: %v", err)
	}
	return resp.Msg
}

func listGoalTransitions(t *testing.T, e *fleetEnv, req *hubv1.ListGoalTransitionsRequest,
) *hubv1.ListGoalTransitionsResponse {
	t.Helper()
	resp, err := e.client.ListGoalTransitions(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListGoalTransitions: %v", err)
	}
	return resp.Msg
}

// TestListGoalRunsServesTheSingleObservedRun is 02k's observed-goal run
// history (03k: no run beyond run 1): the one run, started by hand,
// carrying the goal's machine, session and counters, with no ending
// while the goal is open.
func TestListGoalRunsServesTheSingleObservedRun(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-alpha", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "sess-1", goalBase)

	msg := listGoalRuns(t, e, &hubv1.ListGoalRunsRequest{Goal: &typev1.GoalId{Value: "obs-alpha"}})
	if len(msg.GetRuns()) != 1 {
		t.Fatalf("runs %d, want the single observed run", len(msg.GetRuns()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	run := msg.GetRuns()[0]
	if run.GetGoal().GetValue() != "obs-alpha" {
		t.Errorf("run.goal %q, want obs-alpha", run.GetGoal().GetValue())
	}
	if run.GetNumber() != 1 {
		t.Errorf("run.number %d, want 1 (observed goals never restart)", run.GetNumber())
	}
	if run.GetCause() != hubv1.GoalRunCause_GOAL_RUN_CAUSE_START {
		t.Errorf("run.cause %s, want START (hand-started)", run.GetCause())
	}
	if got := run.GetStartTime(); !got.IsValid() || got.AsTime().UnixMilli() != goalBase.UnixMilli() {
		t.Errorf("run.start_time %v, want the goal's start", got)
	}
	if run.GetEndTime() != nil {
		t.Errorf("run.end_time %v, want unset while the run is current", run.GetEndTime())
	}
	if run.GetEndPhase() != hubv1.GoalPhase_GOAL_PHASE_UNSPECIFIED {
		t.Errorf("run.end_phase %s, want unset while the run is current", run.GetEndPhase())
	}
	if run.GetEndReason() != hubv1.GoalPhaseReason_GOAL_PHASE_REASON_UNSPECIFIED {
		t.Errorf("run.end_reason %s, want unset while the run is current", run.GetEndReason())
	}
	if run.GetMachine().GetValue() != "m-one" {
		t.Errorf("run.machine %q, want m-one", run.GetMachine().GetValue())
	}
	if run.GetSession().GetValue() != "sess-1" {
		t.Errorf("run.session %q, want sess-1", run.GetSession().GetValue())
	}
	if run.GetPane() != nil {
		t.Errorf("run.pane %v, want unset (observed goals store no pane key)", run.GetPane())
	}
	if run.GetNudges() != 0 || run.GetNoProgressRuns() != 0 {
		t.Errorf("run nudges %d no_progress_runs %d, want 0 0 (observed goals are never nudged)",
			run.GetNudges(), run.GetNoProgressRuns())
	}
	if len(run.GetUsageConsumed()) != 0 {
		t.Errorf("run.usage_consumed holds %d entries, want none stored", len(run.GetUsageConsumed()))
	}
}

// TestListGoalRunsClosedGoalCarriesTheEnding covers the end of the single
// observed run: DONE closes it, so the run carries the end time, phase
// and reason.
func TestListGoalRunsClosedGoalCarriesTheEnding(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	start := goalBase.Add(-2 * time.Hour)
	writeClosedObservedGoal(t, e, "obs-done", "m-one", start)

	msg := listGoalRuns(t, e, &hubv1.ListGoalRunsRequest{Goal: &typev1.GoalId{Value: "obs-done"}})
	if len(msg.GetRuns()) != 1 {
		t.Fatalf("runs %d, want 1", len(msg.GetRuns()))
	}
	run := msg.GetRuns()[0]
	if end := run.GetEndTime(); !end.IsValid() ||
		end.AsTime().UnixMilli() != start.Add(time.Hour).UnixMilli() {
		t.Errorf("run.end_time %v, want the close time", end)
	}
	if run.GetEndPhase() != hubv1.GoalPhase_GOAL_PHASE_CLOSED {
		t.Errorf("run.end_phase %s, want CLOSED", run.GetEndPhase())
	}
	if run.GetEndReason() != hubv1.GoalPhaseReason_GOAL_PHASE_REASON_OBSERVED_DONE {
		t.Errorf("run.end_reason %s, want OBSERVED_DONE", run.GetEndReason())
	}
}

// TestGoalHistoryMissesUnknownGoal is the unknown-id path both history
// reads share with GetGoal: RESOURCE_NOT_FOUND, and an empty id is
// INVALID_ARGUMENT.
func TestGoalHistoryMissesUnknownGoal(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-alpha", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase)

	_, err := e.client.ListGoalRuns(context.Background(), connect.NewRequest(&hubv1.ListGoalRunsRequest{
		Goal: &typev1.GoalId{Value: "obs-missing"},
	}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("runs unknown id: got %v %v, want not_found RESOURCE_NOT_FOUND", code, reason)
	}
	_, err = e.client.ListGoalTransitions(context.Background(), connect.NewRequest(
		&hubv1.ListGoalTransitionsRequest{Goal: &typev1.GoalId{Value: "obs-missing"}}))
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("transitions unknown id: got %v %v, want not_found RESOURCE_NOT_FOUND", code, reason)
	}
	_, err = e.client.ListGoalRuns(context.Background(), connect.NewRequest(&hubv1.ListGoalRunsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Errorf("runs empty id: got %v %v, want invalid_argument INVALID_ARGUMENT", code, reason)
	}
	_, err = e.client.ListGoalTransitions(context.Background(), connect.NewRequest(
		&hubv1.ListGoalTransitionsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Errorf("transitions empty id: got %v %v, want invalid_argument INVALID_ARGUMENT", code, reason)
	}
}

// TestListGoalTransitionsIsEmptyUntilDispatchRows pins the row-12 shape of
// the transition list: observed goals record no transitions (03l W6 runs
// only for dispatched goals), so a known goal reads empty, ascending and
// descending alike.
func TestListGoalTransitionsIsEmptyUntilDispatchRows(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-alpha", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase)
	writeClosedObservedGoal(t, e, "obs-done", "m-one", goalBase.Add(-2*time.Hour))

	for _, descending := range []bool{false, true} {
		msg := listGoalTransitions(t, e, &hubv1.ListGoalTransitionsRequest{
			Goal: &typev1.GoalId{Value: "obs-done"}, Descending: descending,
		})
		if len(msg.GetTransitions()) != 0 {
			t.Fatalf("transitions %d (descending %v), want none until dispatch rows land",
				len(msg.GetTransitions()), descending)
		}
		if msg.GetNextPageToken() != "" {
			t.Errorf("next_page_token %q, want empty on an empty list", msg.GetNextPageToken())
		}
	}
}

// TestGoalHistoryPages covers the shared paging: a page that fits carries
// no token, and a token minted for another query is PAGE_TOKEN_INVALID.
func TestGoalHistoryPages(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	writeObservedGoal(t, e, "obs-alpha", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase)
	writeObservedGoal(t, e, "obs-beta", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase)

	msg := listGoalRuns(t, e, &hubv1.ListGoalRunsRequest{
		Goal: &typev1.GoalId{Value: "obs-alpha"}, PageSize: 1,
	})
	if len(msg.GetRuns()) != 1 || msg.GetNextPageToken() != "" {
		t.Fatalf("page holds %d runs token %q, want 1 and no token",
			len(msg.GetRuns()), msg.GetNextPageToken())
	}
	if msg.GetRuns()[0].GetSession() != nil {
		t.Errorf("run.session %v, want unset (no pane session reported)", msg.GetRuns()[0].GetSession())
	}
	_, err := e.client.ListGoalRuns(context.Background(), connect.NewRequest(&hubv1.ListGoalRunsRequest{
		Goal: &typev1.GoalId{Value: "obs-alpha"}, PageSize: 1, PageToken: "not-a-token",
	}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("bad token: got %v %v, want invalid_argument PAGE_TOKEN_INVALID", code, reason)
	}
	other := listGoals(t, e, &hubv1.ListGoalsRequest{PageSize: 1})
	if other.GetNextPageToken() == "" {
		t.Fatal("need a ListGoals token to try as a foreign token")
	}
	_, err = e.client.ListGoalRuns(context.Background(), connect.NewRequest(&hubv1.ListGoalRunsRequest{
		Goal: &typev1.GoalId{Value: "obs-alpha"}, PageSize: 50, PageToken: other.GetNextPageToken(),
	}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: got %v %v, want invalid_argument PAGE_TOKEN_INVALID", code, reason)
	}
	_, err = e.client.ListGoalTransitions(context.Background(), connect.NewRequest(
		&hubv1.ListGoalTransitionsRequest{
			Goal: &typev1.GoalId{Value: "obs-alpha"}, PageToken: "not-a-token",
		}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("transitions bad token: got %v %v, want invalid_argument PAGE_TOKEN_INVALID", code, reason)
	}
}

// TestGoalHistoryReadsDeniedPaths is the 02d READ gate both history reads
// share with the goal reads: only the local user calls until row 12
// brings caller scopes.
func TestGoalHistoryReadsDeniedPaths(t *testing.T) {
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
			_, err := e.client.ListGoalRuns(context.Background(), connect.NewRequest(
				&hubv1.ListGoalRunsRequest{Goal: &typev1.GoalId{Value: "obs-x"}}))
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Errorf("ListGoalRuns: got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
			_, err = e.client.ListGoalTransitions(context.Background(), connect.NewRequest(
				&hubv1.ListGoalTransitionsRequest{Goal: &typev1.GoalId{Value: "obs-x"}}))
			if code, reason, _ := detail(t, err); code != tc.code || reason != tc.reason {
				t.Errorf("ListGoalTransitions: got %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
}

// TestListGoalRunsIsServedOnTheAdminSocket proves the slice's deliverable:
// the run history answers over admin.sock like the other goal reads.
func TestListGoalRunsIsServedOnTheAdminSocket(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, nil)
	writeObservedGoal(t, e, "obs-alpha", "m-one", hubv1.GoalPhase_GOAL_PHASE_RUNNING, "", goalBase)
	c := api.AdminClient(serveAdmin(t, e, nil))

	resp, err := c.ListGoalRuns(context.Background(), connect.NewRequest(&hubv1.ListGoalRunsRequest{
		Goal: &typev1.GoalId{Value: "obs-alpha"},
	}))
	if err != nil {
		t.Fatalf("ListGoalRuns over the socket: %v", err)
	}
	if len(resp.Msg.GetRuns()) != 1 || resp.Msg.GetRuns()[0].GetNumber() != 1 {
		t.Errorf("socket runs %v, want the single observed run", resp.Msg.GetRuns())
	}
	resp2, err := c.ListGoalTransitions(context.Background(), connect.NewRequest(
		&hubv1.ListGoalTransitionsRequest{Goal: &typev1.GoalId{Value: "obs-alpha"}}))
	if err != nil {
		t.Fatalf("ListGoalTransitions over the socket: %v", err)
	}
	if len(resp2.Msg.GetTransitions()) != 0 {
		t.Errorf("socket transitions %d, want none", len(resp2.Msg.GetTransitions()))
	}
}
