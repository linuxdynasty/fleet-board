package api_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/goals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// seedDispatchRepoMode inserts an enabled watched repo with full consent
// and the given hooks mode (REPO_HOOKS_MODE_* short name in protojson).
func seedDispatchRepoMode(t *testing.T, e *fleetEnv, host, path, mode string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO repos(host, path, code_host, spec_json, version,
			create_ms, update_ms, enabled) VALUES (?, ?, 'github', ?, 1, 1, 1, 1)`,
			host, path, `{"enabled":true,"consent":{"acts":["CONSENT_ACT_PUSH","CONSENT_ACT_PR_OPEN","CONSENT_ACT_MERGE"]},"hooksMode":"`+mode+`"}`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// startGoalReqNoText is startGoalReq with an empty objective: the hub
// fills the default for a hook-less repo (issue #423).
func startGoalReqNoText(machine, harness, account string) *hubv1.StartGoalRequest {
	req := startGoalReq(machine, harness, account)
	req.GetInput().GoalText = ""
	return req
}

// goalDefTextOf reads one goal's stored GoalDefinition goal_text.
func goalDefTextOf(t *testing.T, e *fleetEnv, goalID string) string {
	t.Helper()
	var text string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		row, err := statedb.ReadGoalTx(context.Background(), tx, goalID)
		if err != nil {
			return err
		}
		var def hubv1.GoalDefinition
		if err := protojson.Unmarshal([]byte(row.Spec), &def); err != nil {
			return err
		}
		text = def.GetGoalText()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// startGoalCommandTextOf reads the StartGoal command's spec goal_text for
// a goal, proving the fill reaches the agent wire spec, not just storage.
func startGoalCommandTextOf(t *testing.T, e *fleetEnv, goalID string) string {
	t.Helper()
	var text string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		var body []byte
		if err := tx.QueryRowContext(context.Background(),
			`SELECT command FROM commands WHERE goal_id = ?`, goalID).Scan(&body); err != nil {
			return err
		}
		var stored agentv1.Command
		if err := proto.Unmarshal(body, &stored); err != nil {
			return err
		}
		text = stored.GetStartGoal().GetSpec().GetGoalText()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return text
}

// seedDispatchTicketItem mirrors a READY trusted non-issue work item: an
// empty goal text on it stays refused even on a hook-less repo, since the
// fill derives the default from an issue number.
func seedDispatchTicketItem(t *testing.T, e *fleetEnv) {
	t.Helper()
	_, err := statedb.UpsertWorkItemPage(context.Background(), e.st, "github-1",
		[]statedb.WorkItemMirror{{
			Integration: "github-1", Kind: "TICKET", Project: "acme/web", ItemKey: "13579",
			ItemJSON:  []byte(`{"title":"fix the leak"}`),
			UpdateMs:  10,
			Stage:     int32(hubv1.WorkStage_WORK_STAGE_READY),
			TrustJSON: []byte(`{"trusted":true}`),
		}}, nil, nil, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
}

// seedDispatchNonNumericItem mirrors a READY trusted issue whose key holds
// no issue number: the fill cannot derive a default from it, so an empty
// goal text stays refused.
func seedDispatchNonNumericItem(t *testing.T, e *fleetEnv) {
	t.Helper()
	_, err := statedb.UpsertWorkItemPage(context.Background(), e.st, "github-1",
		[]statedb.WorkItemMirror{{
			Integration: "github-1", Kind: "ISSUE", Project: "acme/web", ItemKey: "abc",
			ItemJSON:  []byte(`{"title":"fix the leak"}`),
			UpdateMs:  10,
			Stage:     int32(hubv1.WorkStage_WORK_STAGE_READY),
			TrustJSON: []byte(`{"trusted":true}`),
		}}, nil, nil, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
}

// preflightReqNoText is startGoalReqNoText for PreflightGoal: the same
// input with an empty objective, proving a preflight fills (or refuses)
// exactly like a start.
func preflightReqNoText(machine, harness, account string) *hubv1.PreflightGoalRequest {
	in := startGoalReq(machine, harness, account).GetInput()
	in.GoalText = ""
	return &hubv1.PreflightGoalRequest{RequestId: "req-preflight-1", Input: in}
}

// preflightSpecOf reads the queued Preflight command's spec goal text and
// branch, proving the fill reaches the agent wire spec and naming the
// branch the default text renders from (a preflight mints no goal id).
func preflightSpecOf(t *testing.T, e *fleetEnv, machine string) (text, branch string) {
	t.Helper()
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		var body []byte
		if err := tx.QueryRowContext(context.Background(),
			`SELECT command FROM commands WHERE machine_id = ? ORDER BY id LIMIT 1`, machine).Scan(&body); err != nil {
			return err
		}
		var stored agentv1.Command
		if err := proto.Unmarshal(body, &stored); err != nil {
			return err
		}
		text = stored.GetPreflight().GetSpec().GetGoalText()
		branch = stored.GetPreflight().GetSpec().GetBranch()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return text, branch
}

// seedHooklessDispatch runs the standard seed against a GENERIC repo and
// returns the env: the repo ships no own hooks, so an empty goal text
// must fill from the approved default.
func seedHooklessDispatch(t *testing.T) *fleetEnv {
	t.Helper()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepoMode(t, e, "github.com", "acme/web", "REPO_HOOKS_MODE_GENERIC")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItem(t, e)
	return e
}

// TestStartGoalEmptyTextFillsDefaultForHooklessRepo proves the #423 fill:
// an empty goal_text on a GENERIC repo starts the goal with the approved
// default text, in storage and on the agent's StartGoal spec.
func TestStartGoalEmptyTextFillsDefaultForHooklessRepo(t *testing.T) {
	t.Parallel()
	e := seedHooklessDispatch(t)

	res, err := e.client.StartGoal(context.Background(), connect.NewRequest(startGoalReqNoText("m1", "claude", "a1")))
	if err != nil {
		t.Fatalf("StartGoal with empty text on a hook-less repo: %v", err)
	}
	id := res.Msg.GetGoal().GetValue()
	if id == "" {
		t.Fatal("no goal id in StartGoal response")
	}
	branch := goalBranchOf(t, e, id)
	want, stub := goals.DefaultGoalText("acme/web", 7398, branch)
	if stub {
		t.Fatal("DefaultGoalText stub = true, want the approved text")
	}
	if got := goalDefTextOf(t, e, id); got != want {
		t.Fatalf("stored goal text misses the default fill")
	}
	if got := startGoalCommandTextOf(t, e, id); got != want {
		t.Fatalf("StartGoal spec text misses the default fill")
	}
	if !strings.Contains(want, "acme/web#7398") {
		t.Fatalf("default text misses the work item ref, got %q", want)
	}
}

// TestStartGoalEmptyTextFillsDefaultForUnsetMode proves the fill also
// applies when the repo never set a hooks mode (UNSPECIFIED reads
// hook-less): only ESHU keeps the hard requirement.
func TestStartGoalEmptyTextFillsDefaultForUnsetMode(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepo(t, e, "github.com", "acme/web")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItem(t, e)

	res, err := e.client.StartGoal(context.Background(), connect.NewRequest(startGoalReqNoText("m1", "claude", "a1")))
	if err != nil {
		t.Fatalf("StartGoal with empty text and unset hooks mode: %v", err)
	}
	id := res.Msg.GetGoal().GetValue()
	want, _ := goals.DefaultGoalText("acme/web", 7398, goalBranchOf(t, e, id))
	if got := goalDefTextOf(t, e, id); got != want {
		t.Fatalf("stored goal text misses the default fill for unset hooks mode")
	}
}

// TestStartGoalEmptyTextRefusedForEshuRepo proves hooks repos are
// unaffected: an ESHU repo with an empty goal_text is still refused, so
// the repo's own hook flow keeps its explicit objective.
func TestStartGoalEmptyTextRefusedForEshuRepo(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepoMode(t, e, "github.com", "acme/web", "REPO_HOOKS_MODE_ESHU")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItem(t, e)

	_, err := e.client.StartGoal(context.Background(), connect.NewRequest(startGoalReqNoText("m1", "claude", "a1")))
	if err == nil {
		t.Fatal("StartGoal with empty text on an ESHU repo succeeded, want INVALID_ARGUMENT")
	} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("StartGoal code = %v, want INVALID_ARGUMENT", connect.CodeOf(err))
	}
}

// TestStartGoalExplicitTextUnaffectedOnEshuRepo proves an explicit
// objective on an ESHU repo dispatches verbatim: the fill never rewrites
// what the operator sent.
func TestStartGoalExplicitTextUnaffectedOnEshuRepo(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepoMode(t, e, "github.com", "acme/web", "REPO_HOOKS_MODE_ESHU")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItem(t, e)

	res, err := e.client.StartGoal(context.Background(), connect.NewRequest(startGoalReq("m1", "claude", "a1")))
	if err != nil {
		t.Fatalf("StartGoal with explicit text on an ESHU repo: %v", err)
	}
	id := res.Msg.GetGoal().GetValue()
	if got := goalDefTextOf(t, e, id); got != "fix the leak" {
		t.Fatalf("stored goal text = %q, want the explicit objective", got)
	}
	if got := startGoalCommandTextOf(t, e, id); got != "fix the leak" {
		t.Fatalf("StartGoal spec text = %q, want the explicit objective", got)
	}
}

// TestStartGoalEmptyTextRefusedForNonIssueKind proves the fill only
// derives a default from an issue number: an empty goal text on a
// non-issue work item stays refused even on a hook-less repo.
func TestStartGoalEmptyTextRefusedForNonIssueKind(t *testing.T) {
	t.Parallel()
	e := seedHooklessDispatch(t)
	seedDispatchTicketItem(t, e)

	req := startGoalReqNoText("m1", "claude", "a1")
	req.GetInput().GetWorkItem().Kind = typev1.WorkItemKind_WORK_ITEM_KIND_TICKET
	req.GetInput().GetWorkItem().Key = "13579"
	_, err := e.client.StartGoal(context.Background(), connect.NewRequest(req))
	if err == nil {
		t.Fatal("StartGoal with empty text on a non-issue work item succeeded, want INVALID_ARGUMENT")
	} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("StartGoal code = %v, want INVALID_ARGUMENT", connect.CodeOf(err))
	}
}

// TestStartGoalEmptyTextRefusedForNonNumericKey proves the fill needs an
// issue number in the key: an empty goal text on an issue whose key is
// not numeric stays refused even on a hook-less repo.
func TestStartGoalEmptyTextRefusedForNonNumericKey(t *testing.T) {
	t.Parallel()
	e := seedHooklessDispatch(t)
	seedDispatchNonNumericItem(t, e)

	req := startGoalReqNoText("m1", "claude", "a1")
	req.GetInput().GetWorkItem().Key = "abc"
	_, err := e.client.StartGoal(context.Background(), connect.NewRequest(req))
	if err == nil {
		t.Fatal("StartGoal with empty text on a non-numeric issue key succeeded, want INVALID_ARGUMENT")
	} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("StartGoal code = %v, want INVALID_ARGUMENT", connect.CodeOf(err))
	}
}

// TestPreflightGoalEmptyTextFillsForHooklessRepo proves a preflight fills
// like a start: an empty goal text on a hook-less repo queues a Preflight
// command carrying the approved default text.
func TestPreflightGoalEmptyTextFillsForHooklessRepo(t *testing.T) {
	t.Parallel()
	e := seedHooklessDispatch(t)

	_, err := e.client.PreflightGoal(context.Background(), connect.NewRequest(preflightReqNoText("m1", "claude", "a1")))
	if err != nil {
		t.Fatalf("PreflightGoal with empty text on a hook-less repo: %v", err)
	}
	text, branch := preflightSpecOf(t, e, "m1")
	want, stub := goals.DefaultGoalText("acme/web", 7398, branch)
	if stub {
		t.Fatal("DefaultGoalText stub = true, want the approved text")
	}
	if text != want {
		t.Fatalf("Preflight spec text misses the default fill")
	}
	if !strings.Contains(want, "acme/web#7398") {
		t.Fatalf("default text misses the work item ref, got %q", want)
	}
}

// TestPreflightGoalEmptyTextRefusedForEshuRepo proves a preflight refuses
// like a start: an empty goal text on an ESHU repo is still refused, so
// the repo's own hook flow keeps its explicit objective.
func TestPreflightGoalEmptyTextRefusedForEshuRepo(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchRepoMode(t, e, "github.com", "acme/web", "REPO_HOOKS_MODE_ESHU")
	seedDispatchAccount(t, e, "a1", "claude")
	seedDispatchItem(t, e)

	_, err := e.client.PreflightGoal(context.Background(), connect.NewRequest(preflightReqNoText("m1", "claude", "a1")))
	if err == nil {
		t.Fatal("PreflightGoal with empty text on an ESHU repo succeeded, want INVALID_ARGUMENT")
	} else if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("PreflightGoal code = %v, want INVALID_ARGUMENT", connect.CodeOf(err))
	}
}
