package api_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
)

// hookReq builds an EnsureMachineHooks request with the secret header.
func hookReq(t *testing.T, e *fleetEnv, msg *hubv1.EnsureMachineHooksRequest) *connect.Request[hubv1.EnsureMachineHooksRequest] {
	t.Helper()
	secretMu.Lock()
	defer secretMu.Unlock()
	if _, err := secret.Ensure(e.dir, rand.Read); err != nil {
		t.Fatal(err)
	}
	shown, err := secret.Reveal(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(msg)
	req.Header().Set(api.SecretHeader, shown)
	return req
}

func seedHookRepo(t *testing.T, e *fleetEnv, host, path, mode string) {
	t.Helper()
	seedWatchedRepo(t, e, host, path, "github", mode, true, "req-"+host+path, repoBase)
}

// liveGoal inserts a live (unended) goal row for the machine with the
// harness in its spec.
func liveGoal(t *testing.T, e *fleetEnv, id, machine, harness string) {
	t.Helper()
	spec := `{"harness":{"name":"` + harness + `"}}`
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO goals(id, origin, machine_id, spec, status,
			phase, phase_reason, start_time, end_time, observed_time, hub_seq)
			VALUES (?, 'dispatched', ?, ?, '{}', 'GOAL_PHASE_RUNNING', '', 1, NULL, 1, 1)`, id, machine, spec)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestEnsureMachineHooksFansOut: the button expands over every known
// harness and enabled repo, lands one EnsureGoalHooks per pair in the
// outbox, and audits the call.
func TestEnsureMachineHooksFansOut(t *testing.T) {
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	seedHookRepo(t, e, "github.com", "acme/web", "REPO_HOOKS_MODE_GENERIC")
	res, err := e.client.EnsureMachineHooks(context.Background(), hookReq(t, e, &hubv1.EnsureMachineHooksRequest{
		RequestId: "req-hooks-1", Machine: &typev1.MachineId{Value: "m1"},
	}))
	if err != nil {
		t.Fatalf("EnsureMachineHooks: %v", err)
	}
	if got := len(res.Msg.GetOperations()); got != 4 {
		t.Fatalf("operations = %d, want 4 (hook-shaped harnesses x one repo; opencode takes no install)", got)
	}
	if got := pendingCommands(t, e, "m1"); got != 4 {
		t.Fatalf("outbox holds %d commands, want 4", got)
	}
	if got := auditActionsFor(t, e, "machines/m1"); len(got) != 4 {
		t.Fatalf("audit = %v, want one machine.ensure_hooks per command", got)
	} else {
		for _, a := range got {
			if a != "machine.ensure_hooks" {
				t.Fatalf("audit = %v, want machine.ensure_hooks rows", got)
			}
		}
	}
	// Same request again replays the same operations without new rows.
	res2, err := e.client.EnsureMachineHooks(context.Background(), hookReq(t, e, &hubv1.EnsureMachineHooksRequest{
		RequestId: "req-hooks-1", Machine: &typev1.MachineId{Value: "m1"},
	}))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(res2.Msg.GetOperations()) != 4 {
		t.Fatalf("replay operations = %d, want 4", len(res2.Msg.GetOperations()))
	}
	if got := pendingCommands(t, e, "m1"); got != 4 {
		t.Fatalf("replay enqueued more: outbox holds %d", got)
	}
}

// TestEnsureMachineHooksNarrows: an explicit harness and repo fan out to
// exactly that pair, and a live goal on the harness guards it.
func TestEnsureMachineHooksNarrows(t *testing.T) {
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	seedHookRepo(t, e, "github.com", "acme/web", "REPO_HOOKS_MODE_GENERIC")
	msg := &hubv1.EnsureMachineHooksRequest{
		RequestId: "req-hooks-2", Machine: &typev1.MachineId{Value: "m1"},
		Harness: &typev1.Harness{Name: "codex"},
		Repo:    &typev1.RepoRef{Host: "github.com", Path: "acme/web"},
	}
	res, err := e.client.EnsureMachineHooks(context.Background(), hookReq(t, e, msg))
	if err != nil {
		t.Fatalf("EnsureMachineHooks: %v", err)
	}
	ops := res.Msg.GetOperations()
	if len(ops) != 1 {
		t.Fatalf("operations = %d, want 1", len(ops))
	}
	liveGoal(t, e, "g1", "m1", "codex")
	res, err = e.client.EnsureMachineHooks(context.Background(), hookReq(t, e, &hubv1.EnsureMachineHooksRequest{
		RequestId: "req-hooks-3", Machine: &typev1.MachineId{Value: "m1"},
		Harness: &typev1.Harness{Name: "codex"},
		Repo:    &typev1.RepoRef{Host: "github.com", Path: "acme/web"},
	}))
	if err != nil {
		t.Fatalf("guarded call: %v", err)
	}
	if len(res.Msg.GetOperations()) != 0 {
		t.Fatalf("live goal did not guard: %+v", res.Msg.GetOperations())
	}
}

// TestEnsureMachineHooksRefusals: unknown machine, missing request id,
// missing secret, unknown harness, unwatched repo, and ESHU repos,
// which only report.
func TestEnsureMachineHooksRefusals(t *testing.T) {
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	seedHookRepo(t, e, "github.com", "acme/web", "REPO_HOOKS_MODE_GENERIC")
	seedHookRepo(t, e, "github.com", "acme/eshu", "REPO_HOOKS_MODE_ESHU")
	mk := func(mut func(*hubv1.EnsureMachineHooksRequest)) *connect.Request[hubv1.EnsureMachineHooksRequest] {
		m := &hubv1.EnsureMachineHooksRequest{RequestId: "req-x", Machine: &typev1.MachineId{Value: "m1"}}
		mut(m)
		return hookReq(t, e, m)
	}
	cases := map[string]*connect.Request[hubv1.EnsureMachineHooksRequest]{
		"unknown machine": mk(func(m *hubv1.EnsureMachineHooksRequest) { m.Machine = &typev1.MachineId{Value: "nope"} }),
		"missing request": mk(func(m *hubv1.EnsureMachineHooksRequest) { m.RequestId = "" }),
		"unknown harness": mk(func(m *hubv1.EnsureMachineHooksRequest) { m.Harness = &typev1.Harness{Name: "hal"} }),
		"unwatched repo": mk(func(m *hubv1.EnsureMachineHooksRequest) {
			m.Repo = &typev1.RepoRef{Host: "github.com", Path: "acme/nope"}
		}),
	}
	for name, req := range cases {
		if _, err := e.client.EnsureMachineHooks(context.Background(), req); err == nil {
			t.Errorf("%s: succeeded, want refusal", name)
		}
	}
	// ESHU repos report only: OK with no operations.
	res, err := e.client.EnsureMachineHooks(context.Background(), mk(func(m *hubv1.EnsureMachineHooksRequest) {
		m.Repo = &typev1.RepoRef{Host: "github.com", Path: "acme/eshu"}
	}))
	if err != nil {
		t.Fatalf("eshu: %v", err)
	}
	if len(res.Msg.GetOperations()) != 0 {
		t.Fatalf("eshu repo enqueued: %+v", res.Msg.GetOperations())
	}
	// No secret: denied.
	plain := connect.NewRequest(&hubv1.EnsureMachineHooksRequest{RequestId: "req-nosec", Machine: &typev1.MachineId{Value: "m1"}})
	if _, err := e.client.EnsureMachineHooks(context.Background(), plain); err == nil {
		t.Fatalf("missing secret succeeded")
	}
}

// TestEnsureMachineHooksNeedsSettings: a signed-in user holds no
// SETTINGS scope (02d).
func TestEnsureMachineHooksNeedsSettings(t *testing.T) {
	e := newFleetEnv(t, &typev1.Principal{
		Id: &typev1.PrincipalId{Value: "u-bob"}, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER, Name: "bob",
	})
	_, err := e.client.EnsureMachineHooks(context.Background(), connect.NewRequest(
		&hubv1.EnsureMachineHooksRequest{RequestId: "req-bob", Machine: &typev1.MachineId{Value: "m1"}}))
	code, reason, _ := detail(t, err)
	if code != connect.CodePermissionDenied || reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Fatalf("got %v %v, want permission_denied SCOPE_MISSING", code, reason)
	}
}

// TestEnsureMachineHooksProfileFansOut: a named profile reaches the
// forced keys, so reinstalling hooks for that profile queues commands.
func TestEnsureMachineHooksProfileFansOut(t *testing.T) {
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	seedHookRepo(t, e, "github.com", "acme/web", "REPO_HOOKS_MODE_GENERIC")
	msg := &hubv1.EnsureMachineHooksRequest{
		RequestId: "req-hooks-profile", Machine: &typev1.MachineId{Value: "m1"},
		Harness: &typev1.Harness{Name: "codex"}, Profile: "work",
		Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/web"},
	}
	res, err := e.client.EnsureMachineHooks(context.Background(), hookReq(t, e, msg))
	if err != nil {
		t.Fatalf("EnsureMachineHooks: %v", err)
	}
	if got := len(res.Msg.GetOperations()); got != 1 {
		t.Fatalf("operations = %d, want 1 (codex/work profile)", got)
	}
}
