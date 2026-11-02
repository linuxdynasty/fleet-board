package api_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// probeKinds reads the outbox command kinds queued for a machine.
func probeKinds(t *testing.T, e *fleetEnv, machine string) []string {
	t.Helper()
	var out []string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(),
			`SELECT kind FROM commands WHERE machine_id = ? ORDER BY create_time`, machine)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var kind string
			if err := rows.Scan(&kind); err != nil {
				return err
			}
			out = append(out, kind)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func probeAccount(t *testing.T, e *fleetEnv, id, requestID string) (*connect.Response[hubv1.ProbeAccountResponse], error) {
	t.Helper()
	return e.client.ProbeAccount(context.Background(), connect.NewRequest(&hubv1.ProbeAccountRequest{
		RequestId: requestID,
		Id:        &typev1.AccountId{Value: id},
	}))
}

// TestProbeAccountQueuesRunNow is the 02b probe path: the account's
// probe runs now on an ONLINE machine with a launch-allowed harness,
// and the response carries the PENDING operation for it.
func TestProbeAccountQueuesRunNow(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchHarness(t, e, "m1", "claude")
	seedDispatchAccount(t, e, "a1", "claude")

	res, err := probeAccount(t, e, "a1", "req-probe-1")
	if err != nil {
		t.Fatalf("ProbeAccount: %v", err)
	}
	if got := res.Msg.GetOperation().GetMachine().GetValue(); got != "m1" {
		t.Errorf("operation machine = %q, want m1", got)
	}
	if got := res.Msg.GetOperation().GetState(); got != hubv1.OperationState_OPERATION_STATE_PENDING {
		t.Errorf("operation state = %v, want PENDING", got)
	}
	if kinds := probeKinds(t, e, "m1"); len(kinds) != 1 || kinds[0] != "ensure_probe" {
		t.Errorf("outbox kinds = %v, want one ensure_probe", kinds)
	}
}

// TestProbeAccountUnknownAccount refuses an account the hub never saw.
func TestProbeAccountUnknownAccount(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))

	_, err := probeAccount(t, e, "nope", "req-probe-1")
	if code, why, _ := detail(t, err); code != connect.CodeNotFound ||
		why != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("probe unknown: %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, why)
	}
}

// TestProbeAccountNeedsAFitMachine refuses when no ONLINE machine may
// launch the harness: a probe with nowhere to run never queues.
func TestProbeAccountNeedsAFitMachine(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedDispatchAccount(t, e, "a1", "claude")

	_, err := probeAccount(t, e, "a1", "req-probe-1")
	if code, why, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
		why != typev1.ReasonCode_REASON_CODE_NO_FIT_MACHINE {
		t.Errorf("probe no machine: %v %v, want FAILED_PRECONDITION NO_FIT_MACHINE", code, why)
	}
	if kinds := probeKinds(t, e, "m1"); len(kinds) != 0 {
		t.Errorf("outbox kinds = %v, want nothing queued", kinds)
	}
}

// TestProbeAccountReplaysRequestId proves idempotency: repeating the
// same request_id returns the stored operation instead of queueing a
// second probe.
func TestProbeAccountReplaysRequestId(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchHarness(t, e, "m1", "claude")
	seedDispatchAccount(t, e, "a1", "claude")

	first, err := probeAccount(t, e, "a1", "req-probe-same")
	if err != nil {
		t.Fatalf("first ProbeAccount: %v", err)
	}
	second, err := probeAccount(t, e, "a1", "req-probe-same")
	if err != nil {
		t.Fatalf("second ProbeAccount: %v", err)
	}
	if first.Msg.GetOperation().GetId().GetValue() != second.Msg.GetOperation().GetId().GetValue() {
		t.Error("replay returned a different operation, want the stored one")
	}
	if kinds := probeKinds(t, e, "m1"); len(kinds) != 1 {
		t.Errorf("outbox kinds = %v, want one ensure_probe: no duplicate", kinds)
	}
}

// TestProbeAccountRunNowLimited proves the one-a-minute gate: a second
// probe inside the minute is refused, so a stuck caller cannot flood
// the machine with probes.
func TestProbeAccountRunNowLimited(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchHarness(t, e, "m1", "claude")
	seedDispatchAccount(t, e, "a1", "claude")

	if _, err := probeAccount(t, e, "a1", "req-probe-1"); err != nil {
		t.Fatalf("first ProbeAccount: %v", err)
	}
	_, err := probeAccount(t, e, "a1", "req-probe-2")
	if code, why, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
		why != typev1.ReasonCode_REASON_CODE_RATE_LIMITED {
		t.Errorf("second probe: %v %v, want FAILED_PRECONDITION RATE_LIMITED", code, why)
	}
	if kinds := probeKinds(t, e, "m1"); len(kinds) != 1 {
		t.Errorf("outbox kinds = %v, want still one ensure_probe", kinds)
	}
}

// TestProbeAccountDenied proves the SESSIONS_WRITE scope: no caller, no
// probe.
func TestProbeAccountDenied(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	seedDispatchAccount(t, e, "a1", "claude")

	anon := newFleetEnv(t, nil)
	_, err := anon.client.ProbeAccount(context.Background(), connect.NewRequest(&hubv1.ProbeAccountRequest{
		RequestId: "req-probe-1",
		Id:        &typev1.AccountId{Value: "a1"},
	}))
	if code, _, _ := detail(t, err); code != connect.CodeUnauthenticated {
		t.Errorf("probe anon: %v, want UNAUTHENTICATED", code)
	}
}
