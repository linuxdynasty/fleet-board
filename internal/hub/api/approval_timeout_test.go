package api_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestGatedCommandUsesApprovalTimeoutSetting: a gated command expires
// after the fleet layer's spec.hub.approval_timeout, not the default.
func TestGatedCommandUsesApprovalTimeoutSetting(t *testing.T) {
	t.Parallel()
	now := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), envOpt{now: func() time.Time { return now }})
	updateFleetSettings(t, e, "req-settings-1", &hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub:   &hubv1.HubSettings{ApprovalTimeout: durationpb.New(5 * time.Minute)},
	}, []string{"spec.hub.approval_timeout"}, 0)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatalf("RunHarnessCommand: %v", err)
	}
	got, err := e.client.GetApproval(context.Background(), connect.NewRequest(&hubv1.GetApprovalRequest{
		Id: &hubv1.ApprovalId{Value: gated.Msg.GetApproval().GetValue()},
	}))
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if want := now.Add(5 * time.Minute); !got.Msg.GetApproval().GetExpireTime().AsTime().Equal(want) {
		t.Fatalf("expire_time = %v, want %v", got.Msg.GetApproval().GetExpireTime().AsTime(), want)
	}
}

// TestGatedCommandDefaultsApprovalTimeout: with no fleet layer value a
// gated command waits the documented 30 minutes (defaults.md).
func TestGatedCommandDefaultsApprovalTimeout(t *testing.T) {
	t.Parallel()
	now := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), envOpt{now: func() time.Time { return now }})
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatalf("RunHarnessCommand: %v", err)
	}
	got, err := e.client.GetApproval(context.Background(), connect.NewRequest(&hubv1.GetApprovalRequest{
		Id: &hubv1.ApprovalId{Value: gated.Msg.GetApproval().GetValue()},
	}))
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if want := now.Add(30 * time.Minute); !got.Msg.GetApproval().GetExpireTime().AsTime().Equal(want) {
		t.Fatalf("expire_time = %v, want %v", got.Msg.GetApproval().GetExpireTime().AsTime(), want)
	}
}

// TestGatedCommandZeroTimeoutFallsBack: a zero timeout is unstorable
// (the store requires expire_time after now), so gating falls back to
// the documented default instead of failing.
func TestGatedCommandZeroTimeoutFallsBack(t *testing.T) {
	t.Parallel()
	now := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), envOpt{now: func() time.Time { return now }})
	updateFleetSettings(t, e, "req-settings-1", &hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub:   &hubv1.HubSettings{ApprovalTimeout: durationpb.New(0)},
	}, []string{"spec.hub.approval_timeout"}, 0)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatalf("RunHarnessCommand: %v", err)
	}
	got, err := e.client.GetApproval(context.Background(), connect.NewRequest(&hubv1.GetApprovalRequest{
		Id: &hubv1.ApprovalId{Value: gated.Msg.GetApproval().GetValue()},
	}))
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if want := now.Add(30 * time.Minute); !got.Msg.GetApproval().GetExpireTime().AsTime().Equal(want) {
		t.Fatalf("expire_time = %v, want %v", got.Msg.GetApproval().GetExpireTime().AsTime(), want)
	}
}

// TestGatedCommandRefusesCorruptTimeout: a fleet layer the hub cannot
// decode fails the gating call closed (INTERNAL, nothing raised) instead
// of guessing an expiry.
func TestGatedCommandRefusesCorruptTimeout(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO settings_layers(scope, spec_json, version, create_ms, update_ms, hub_seq)
			VALUES ('fleet', '{{{', 1, ?, ?, 0)`, fleetNow.UnixMilli(), fleetNow.UnixMilli())
		return err
	}); err != nil {
		t.Fatalf("seed corrupt layer: %v", err)
	}
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	if _, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess))); err == nil {
		t.Fatal("gating over a corrupt fleet layer succeeded")
	} else if code, _, _ := detail(t, err); code != connect.CodeInternal {
		t.Fatalf("gating: %v, want CodeInternal", code)
	}
	listed, err := e.client.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{}))
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(listed.Msg.GetApprovals()) != 0 {
		t.Fatalf("list = %d approvals, want 0: nothing is raised when the timeout is unreadable",
			len(listed.Msg.GetApprovals()))
	}
}

// TestConfirmDeniedAtConfiguredTimeout: past the configured
// approval_timeout the DENY fallback has applied, so deciding fails
// closed and nothing was ever sent.
func TestConfirmDeniedAtConfiguredTimeout(t *testing.T) {
	t.Parallel()
	now := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), envOpt{now: func() time.Time { return now }})
	updateFleetSettings(t, e, "req-settings-1", &hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub:   &hubv1.HubSettings{ApprovalTimeout: durationpb.New(5 * time.Minute)},
	}, []string{"spec.hub.approval_timeout"}, 0)
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatalf("RunHarnessCommand: %v", err)
	}
	id := gated.Msg.GetApproval().GetValue()
	before := pendingCommands(t, e, "m1")
	now = fleetNow.Add(6 * time.Minute) // past the configured timeout
	_, err = e.client.DecideApproval(context.Background(), connect.NewRequest(
		&hubv1.DecideApprovalRequest{
			RequestId:       "req-decide-1",
			Id:              &hubv1.ApprovalId{Value: id},
			ExpectedVersion: 1,
			Decision:        "approve",
		}))
	if err == nil {
		t.Fatal("decide after the configured expiry succeeded")
	}
	if code, reason, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
		reason != typev1.ReasonCode_REASON_CODE_APPROVAL_CLOSED {
		t.Fatalf("late decide: %v %v, want FailedPrecondition APPROVAL_CLOSED", code, reason)
	}
	if got := pendingCommands(t, e, "m1"); got != before {
		t.Fatalf("outbox holds %d commands, want %d: expired approvals send nothing", got, before)
	}
}
