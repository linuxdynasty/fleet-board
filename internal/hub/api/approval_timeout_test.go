package api_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// seedLayer writes a fleet settings layer straight to the store, for
// values the validated settings path cannot express (corrupt or
// out-of-range serializations a hand edit could still leave behind).
func seedLayer(t *testing.T, e *fleetEnv, spec *hubv1.SettingsSpec) {
	t.Helper()
	raw, err := protojson.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal layer: %v", err)
	}
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO settings_layers(scope, spec_json, version, create_ms, update_ms, hub_seq)
			VALUES ('fleet', ?, 1, ?, ?, 0)`, string(raw), fleetNow.UnixMilli(), fleetNow.UnixMilli())
		return err
	}); err != nil {
		t.Fatalf("seed layer: %v", err)
	}
}

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

// TestGatedCommandRefusesOverflowingTimeout: a fleet layer with an
// approval_timeout time.Duration cannot represent (past ~292 years)
// fails the gating call instead of wrapping negative and silently
// falling back to the default. Written straight to the store because
// only a hand edit can put such a value past the validated settings
// path, which rejects negative serializations.
func TestGatedCommandRefusesOverflowingTimeout(t *testing.T) {
	t.Parallel()
	raw, err := protojson.Marshal(&hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub: &hubv1.HubSettings{ApprovalTimeout: &durationpb.Duration{
			Seconds: int64(300) * 365 * 24 * 3600, // ~300 years
		}},
	})
	if err != nil {
		t.Fatalf("marshal overflowing layer: %v", err)
	}
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO settings_layers(scope, spec_json, version, create_ms, update_ms, hub_seq)
			VALUES ('fleet', ?, 1, ?, ?, 0)`, string(raw), fleetNow.UnixMilli(), fleetNow.UnixMilli())
		return err
	}); err != nil {
		t.Fatalf("seed overflowing layer: %v", err)
	}
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	if _, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess))); err == nil {
		t.Fatal("gating over an overflowing timeout succeeded")
	} else if code, _, _ := detail(t, err); code != connect.CodeInternal {
		t.Fatalf("gating: %v, want CodeInternal", code)
	}
	listed, err := e.client.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{}))
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(listed.Msg.GetApprovals()) != 0 {
		t.Fatalf("list = %d approvals, want 0: nothing is raised when the timeout overflows",
			len(listed.Msg.GetApprovals()))
	}
}

// TestGatedCommandRefusesBoundaryNanosOverflow: 9223372036 seconds with
// nanos past 854775807 already exceeds the int64 range, so it must fail
// closed even though the bare-seconds guard lets that second through.
func TestGatedCommandRefusesBoundaryNanosOverflow(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedLayer(t, e, &hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub: &hubv1.HubSettings{ApprovalTimeout: &durationpb.Duration{
			Seconds: 9223372036, Nanos: 900000000,
		}},
	})
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	if _, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess))); err == nil {
		t.Fatal("gating over a boundary-overflowing timeout succeeded")
	} else if code, _, _ := detail(t, err); code != connect.CodeInternal {
		t.Fatalf("gating: %v, want CodeInternal", code)
	}
	listed, err := e.client.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{}))
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(listed.Msg.GetApprovals()) != 0 {
		t.Fatalf("list = %d approvals, want 0: nothing is raised on boundary overflow",
			len(listed.Msg.GetApprovals()))
	}
}

// TestGatedCommandHonorsBoundarySecond: the exact ceiling
// (9223372036s + 854775807ns) still fits time.Duration, so a hand-set
// layer there must raise a live approval stamped with the full ceiling
// wait, not fail and not silently stamp the 30-minute default.
func TestGatedCommandHonorsBoundarySecond(t *testing.T) {
	t.Parallel()
	now := fleetNow
	e := newFleetEnv(t, localUser("alice"), trusted(false), envOpt{now: func() time.Time { return now }})
	seedLayer(t, e, &hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub: &hubv1.HubSettings{ApprovalTimeout: &durationpb.Duration{
			Seconds: 9223372036, Nanos: 854775807,
		}},
	})
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatalf("RunHarnessCommand at the ceiling: %v", err)
	}
	if gated.Msg.GetApproval().GetValue() == "" {
		t.Fatal("no approval raised at the representable ceiling")
	}
	got, err := e.client.GetApproval(context.Background(), connect.NewRequest(&hubv1.GetApprovalRequest{
		Id: &hubv1.ApprovalId{Value: gated.Msg.GetApproval().GetValue()},
	}))
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	ceiling := time.Duration(9223372036)*time.Second + 854775807*time.Nanosecond
	// The store keeps millisecond precision, so compare against the
	// truncated stamp. What pins the fix is the ~292-year wait versus
	// the 30-minute default, not the sub-millisecond tail.
	if want := now.Add(ceiling).Truncate(time.Millisecond); !got.Msg.GetApproval().GetExpireTime().AsTime().Equal(want) {
		t.Fatalf("expire_time = %v, want %v: the ceiling wait is stamped, not the default",
			got.Msg.GetApproval().GetExpireTime().AsTime(), want)
	}
	listed, err := e.client.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{}))
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(listed.Msg.GetApprovals()) != 1 {
		t.Fatalf("list = %d approvals, want 1: the ceiling wait is honored, not refused",
			len(listed.Msg.GetApprovals()))
	}
}

// TestGatedCommandRefusesNegativeOverflowingTimeout: a hugely negative
// timeout wraps AsDuration positive and would be honored as a ~284-year
// wait, contradicting the non-positive fallback promise. It must fail
// the gating call instead. Seeded straight to the store: the wrapped
// positive also slips past the validated settings path, which only
// rejects negative serializations.
func TestGatedCommandRefusesNegativeOverflowingTimeout(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedLayer(t, e, &hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub: &hubv1.HubSettings{ApprovalTimeout: &durationpb.Duration{
			Seconds: int64(-300) * 365 * 24 * 3600, // ~-300 years
		}},
	})
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	if _, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess))); err == nil {
		t.Fatal("gating over a negative-overflowing timeout succeeded")
	} else if code, _, _ := detail(t, err); code != connect.CodeInternal {
		t.Fatalf("gating: %v, want CodeInternal", code)
	}
	listed, err := e.client.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{}))
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(listed.Msg.GetApprovals()) != 0 {
		t.Fatalf("list = %d approvals, want 0: nothing is raised on negative overflow",
			len(listed.Msg.GetApprovals()))
	}
}

// TestGatedCommandHonorsNegativeFloor: the exact floor
// (-9223372036s - 854775808ns) still fits time.Duration, so it reaches
// the non-positive branch and falls back to the default: an approval
// is raised, not refused.
func TestGatedCommandHonorsNegativeFloor(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedLayer(t, e, &hubv1.SettingsSpec{
		Scope: fleetScope(),
		Hub: &hubv1.HubSettings{ApprovalTimeout: &durationpb.Duration{
			Seconds: -9223372036, Nanos: -854775808,
		}},
	})
	enrollMachine(t, e, "m1")
	sess := createSession(t, e, "m1", "work")
	gated, err := e.client.RunHarnessCommand(context.Background(), connect.NewRequest(destructiveReq(sess)))
	if err != nil {
		t.Fatalf("RunHarnessCommand at the negative floor: %v", err)
	}
	if gated.Msg.GetApproval().GetValue() == "" {
		t.Fatal("no approval raised at the representable negative floor")
	}
	listed, err := e.client.ListApprovals(context.Background(), connect.NewRequest(&hubv1.ListApprovalsRequest{}))
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(listed.Msg.GetApprovals()) != 1 {
		t.Fatalf("list = %d approvals, want 1: the floor wait falls back, not refused",
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
