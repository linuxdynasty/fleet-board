package api

import (
	"context"
	"errors"
	"sort"
	"time"

	"connectrpc.com/connect"

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
	verdicts, err := f.systemVerdicts(ctx, now)
	if err != nil {
		return nil, f.sysStatusFailed(ctx, actor, "store", err)
	}
	return connect.NewResponse(&hubv1.GetSystemStatusResponse{
		Status: system.Snapshot(now, buildinfo.Version, verdicts),
	}), nil
}

// systemVerdicts enumerates one UNKNOWN verdict per (kind, subject): the
// hub-wide signals, then per machine, account and integration in id order,
// so the served list is stable.
func (f *Fleet) systemVerdicts(ctx context.Context, now time.Time) ([]system.Verdict, error) {
	var out []system.Verdict
	for _, kind := range hubSignals {
		out = append(out, system.Verdict{Kind: kind, State: hubv1.SystemState_SYSTEM_STATE_UNKNOWN, Since: now})
	}
	machines, err := statedb.ListMachineRows(ctx, f.store)
	if err != nil {
		return nil, err
	}
	sort.Slice(machines, func(i, j int) bool { return machines[i].MachineID < machines[j].MachineID })
	for _, m := range machines {
		for _, kind := range machineSignals {
			out = append(out, system.Verdict{
				Kind:    kind,
				Machine: &typev1.MachineId{Value: m.MachineID},
				State:   hubv1.SystemState_SYSTEM_STATE_UNKNOWN, Since: now,
			})
		}
	}
	accounts, err := statedb.ListAccountIDs(ctx, f.store)
	if err != nil {
		return nil, err
	}
	for _, id := range accounts {
		out = append(out, system.Verdict{
			Kind:    hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS,
			Account: &typev1.AccountId{Value: id},
			State:   hubv1.SystemState_SYSTEM_STATE_UNKNOWN, Since: now,
		})
	}
	integrations, err := statedb.ListIntegrationRows(ctx, f.store)
	if err != nil {
		return nil, err
	}
	for _, in := range integrations {
		out = append(out, system.Verdict{
			Kind:        hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG,
			Integration: &typev1.IntegrationId{Value: in.ID},
			State:       hubv1.SystemState_SYSTEM_STATE_UNKNOWN, Since: now,
		})
	}
	return out, nil
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
