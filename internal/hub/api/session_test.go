package api_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// enrollMachine inserts an ENROLLED machines row for outbox.Enqueue.
func enrollMachine(t *testing.T, e *fleetEnv, id string) {
	t.Helper()
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO machines(machine_id, name,
			expected_hostname, drained, install_id, state, hostname_mismatch, created_time)
			VALUES (?, ?, ?, 0, 'i', 'ENROLLED', 0, 1)`, id, id, id)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func sendKeysReq(session, pane string) *hubv1.SendKeysRequest {
	return &hubv1.SendKeysRequest{
		RequestId: "req-keys-1",
		Session:   &typev1.SessionId{Value: session},
		Pane:      &typev1.PaneKey{Server: "fleet", ServerStartId: "s1", PaneId: pane},
		Keys:      []*typev1.KeyInput{{Input: &typev1.KeyInput_Literal{Literal: "hi"}}},
	}
}

// TestSendKeysSends: a session the hub started accepts operator text, which
// lands in the agent outbox as one Operation, audited as session.keys.
func TestSendKeysSends(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	res, err := e.client.SendKeys(context.Background(), connect.NewRequest(sendKeysReq(sess, "%1")))
	if err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	op := res.Msg.GetOperation()
	if op.GetId().GetValue() == "" {
		t.Fatal("no operation id")
	}
	if op.GetState() != hubv1.OperationState_OPERATION_STATE_PENDING {
		t.Fatalf("operation state = %v, want PENDING", op.GetState())
	}
	// Two commands: the EnsureSession from CreateSession and this SendKeys.
	if got := pendingCommands(t, e, "m1"); got != 2 {
		t.Fatalf("outbox holds %d commands for m1, want 2", got)
	}
	if got := auditActionsFor(t, e, "sessions/"+sess); len(got) != 2 {
		t.Fatalf("audit for the session = %v, want [session.create session.keys]", got)
	}
}

// createSession starts a hub session on machine for tests.
func createSession(t *testing.T, e *fleetEnv, machine, name string) string {
	t.Helper()
	res, err := e.client.CreateSession(context.Background(), connect.NewRequest(&hubv1.CreateSessionRequest{
		RequestId: "req-create-" + name,
		Machine:   &typev1.MachineId{Value: machine},
		Name:      name,
		Server:    typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET,
	}))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return res.Msg.GetSession().GetValue()
}

// pendingCommands counts pending outbox rows for a machine.
func pendingCommands(t *testing.T, e *fleetEnv, machine string) int {
	t.Helper()
	var n int
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(context.Background(),
			`SELECT count(*) FROM commands WHERE machine_id = ? AND state = 'pending'`, machine).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// auditActionsFor lists audit actions for a target, in order.
func auditActionsFor(t *testing.T, e *fleetEnv, target string) []string {
	t.Helper()
	var out []string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(),
			`SELECT action FROM audit_log WHERE target = ? ORDER BY rowid`, target)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
