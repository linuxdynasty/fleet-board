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

// TestProbeAccountQueuesRunNow stood for the 02b probe path while the
// hub queued keyless probes the agent refuses; it is superseded by
// TestProbeAccountRefusesWithoutStoredKey, which pins the honest
// refusal on the same seed.

// TestProbeAccountRefusesWithoutStoredKey: the hub stores no account
// keys, so it cannot build a probe the agent accepts (the agent refuses
// an empty key). It refuses NO_FIT_MACHINE and queues nothing instead
// of a doomed command.
func TestProbeAccountRefusesWithoutStoredKey(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchHarness(t, e, "m1", "claude")
	seedDispatchAccount(t, e, "a1", "claude")

	_, err := probeAccount(t, e, "a1", "req-probe-1")
	if code, why, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
		why != typev1.ReasonCode_REASON_CODE_NO_FIT_MACHINE {
		t.Errorf("probe no key: %v %v, want FAILED_PRECONDITION NO_FIT_MACHINE", code, why)
	}
	if kinds := probeKinds(t, e, "m1"); len(kinds) != 0 {
		t.Errorf("outbox kinds = %v, want nothing queued", kinds)
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

// TestProbeAccountRepeatedRefusalQueuesNothing: with no stored keys,
// every call is refused alike and nothing queues, so a stuck caller
// cannot pile up doomed probes. (Replay of a past success still works
// through checkProbeReplay; no success is reachable until keys land.)
func TestProbeAccountRepeatedRefusalQueuesNothing(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchHarness(t, e, "m1", "claude")
	seedDispatchAccount(t, e, "a1", "claude")

	for _, id := range []string{"req-probe-same", "req-probe-same"} {
		_, err := probeAccount(t, e, "a1", id)
		if code, why, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
			why != typev1.ReasonCode_REASON_CODE_NO_FIT_MACHINE {
			t.Fatalf("probe %s: %v %v, want FAILED_PRECONDITION NO_FIT_MACHINE", id, code, why)
		}
	}
	if kinds := probeKinds(t, e, "m1"); len(kinds) != 0 {
		t.Errorf("outbox kinds = %v, want nothing queued", kinds)
	}
}

// TestProbeAccountRefusedBeforeRateLimit: the missing-key refusal comes
// before the one-a-minute gate, so rapid repeats stay NO_FIT_MACHINE
// and never reach RATE_LIMITED.
func TestProbeAccountRefusedBeforeRateLimit(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollMachine(t, e, "m1")
	setMachineState(t, e, "m1", "ONLINE")
	seedDispatchHarness(t, e, "m1", "claude")
	seedDispatchAccount(t, e, "a1", "claude")

	for _, id := range []string{"req-probe-1", "req-probe-2"} {
		_, err := probeAccount(t, e, "a1", id)
		if code, why, _ := detail(t, err); code != connect.CodeFailedPrecondition ||
			why != typev1.ReasonCode_REASON_CODE_NO_FIT_MACHINE {
			t.Fatalf("probe %s: %v %v, want FAILED_PRECONDITION NO_FIT_MACHINE", id, code, why)
		}
	}
	if kinds := probeKinds(t, e, "m1"); len(kinds) != 0 {
		t.Errorf("outbox kinds = %v, want nothing queued", kinds)
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
