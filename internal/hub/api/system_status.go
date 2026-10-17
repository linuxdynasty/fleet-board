package api

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
)

// hubSignals are the hub-wide signals in enum order: the writer, the Watch
// fan-out, the log store, the disk and the backups.
var hubSignals = []hubv1.SystemSignalKind{
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE,
}

// machineSignals are the per-machine signals in enum order.
var machineSignals = []hubv1.SystemSignalKind{
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG,
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW,
}

// GetSystemStatus reads the hub's own health (02k: exactly what is served).
// The read needs the same local caller as the machine reads: the admin
// socket serves local_trusted callers, and scopes arrive with the rest of
// row 12.
//
// Unserved so far: the readings behind each signal (every verdict is
// UNKNOWN with its value unset until the evaluation loop lands), the open
// triage item of each signal, hub_start_time, exporters and
// watch_subscribers. They stay empty until their slice.
func (f *Fleet) GetSystemStatus(ctx context.Context, _ *connect.Request[hubv1.GetSystemStatusRequest],
) (*connect.Response[hubv1.GetSystemStatusResponse], error) {
	who, err := authorizeMachines(ctx)
	if err != nil {
		return nil, f.sysStatusRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	now := f.hubNow()
	verdicts, step, err := f.systemVerdicts(ctx)
	if err != nil {
		return nil, f.sysStatusFailed(ctx, actor, step, err)
	}
	return connect.NewResponse(&hubv1.GetSystemStatusResponse{
		Status: system.Snapshot(now, buildinfo.Version, verdicts),
	}), nil
}

// systemVerdicts enumerates one UNKNOWN verdict per (kind, subject): the
// hub-wide signals, then per machine, per FLEET-mode account and per
// integration in id order, so the served list is stable. INTERACTIVE_ONLY
// accounts are never probed (05b section 8), so they raise no freshness
// signal. No verdict carries Since yet: since_time means when the state
// last changed, and no transition is known until the evaluation loop
// lands, so it stays unset rather than stamping every poll. The step names
// the failure for the status log (store, decode).
func (f *Fleet) systemVerdicts(ctx context.Context) ([]system.Verdict, string, error) {
	var out []system.Verdict
	for _, kind := range hubSignals {
		out = append(out, system.Verdict{Kind: kind, State: hubv1.SystemState_SYSTEM_STATE_UNKNOWN})
	}
	machines, err := statedb.ListMachineRows(ctx, f.store)
	if err != nil {
		return nil, "store", err
	}
	sort.Slice(machines, func(i, j int) bool { return machines[i].MachineID < machines[j].MachineID })
	for _, m := range machines {
		for _, kind := range machineSignals {
			out = append(out, system.Verdict{
				Kind:    kind,
				Machine: &typev1.MachineId{Value: m.MachineID},
				State:   hubv1.SystemState_SYSTEM_STATE_UNKNOWN,
			})
		}
	}
	accounts, err := statedb.ListAccountRows(ctx, f.store)
	if err != nil {
		return nil, "store", err
	}
	for _, a := range accounts {
		probed, err := accountProbed(a.Spec)
		if err != nil {
			return nil, "decode", fmt.Errorf("api: system status: account %s: %w", a.ID, err)
		}
		if !probed {
			continue
		}
		out = append(out, system.Verdict{
			Kind:    hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS,
			Account: &typev1.AccountId{Value: a.ID},
			State:   hubv1.SystemState_SYSTEM_STATE_UNKNOWN,
		})
	}
	integrations, err := statedb.ListIntegrationRows(ctx, f.store)
	if err != nil {
		return nil, "store", err
	}
	for _, in := range integrations {
		out = append(out, system.Verdict{
			Kind:        hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG,
			Integration: &typev1.IntegrationId{Value: in.ID},
			State:       hubv1.SystemState_SYSTEM_STATE_UNKNOWN,
		})
	}
	return out, "", nil
}

// accountProbed reports whether the stored account spec belongs to a probed
// account: every mode but INTERACTIVE_ONLY (pacing's excludedCause agrees:
// only that mode never dispatches, and 03b probes nothing with probing
// off). A spec the hub cannot decode is the hub's failure, INTERNAL.
func accountProbed(specJSON []byte) (bool, error) {
	var spec hubv1.AccountSpec
	if err := protojson.Unmarshal(specJSON, &spec); err != nil {
		return false, err
	}
	return spec.GetMode() != hubv1.AccountMode_ACCOUNT_MODE_INTERACTIVE_ONLY, nil
}

// sysStatusRefused logs a GetSystemStatus caller error by its reason and
// returns it.
func (f *Fleet) sysStatusRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "system.issue_refused", "reason", reasonOf(err))
	return err
}

// sysStatusFailed logs the hub's own status-read failure and returns
// INTERNAL without the cause's text.
func (f *Fleet) sysStatusFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "system.status_failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its system status", nil, nil)
}
