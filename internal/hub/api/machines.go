package api

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// authorizeMachines admits the checked admin user or local TCP owner for
// ListMachines and GetMachine (02d: READ).
func authorizeMachines(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// ListMachines lists enrolled machines (02k): the spec as enrolled and the
// stored status. Filter and order_by follow the 02c Machine catalogue
// (machineFilterFields names what this slice serves); page_size defaults to
// 50 and caps at 500, and the page token is bound to its query for 10 min.
func (f *Fleet) ListMachines(ctx context.Context, req *connect.Request[hubv1.ListMachinesRequest],
) (*connect.Response[hubv1.ListMachinesResponse], error) {
	who, err := authorizeMachines(ctx)
	if err != nil {
		return nil, f.machRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	m := req.Msg
	node, ferr := parseFilter(m.GetFilter(), machineFilterFields)
	if ferr != nil {
		return nil, f.machRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
			map[string]string{"offset": fmt.Sprint(ferr.offset)},
			[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
	}
	order, oerr := parseOrderBy(m.GetOrderBy(), machineOrderFields)
	if oerr != nil {
		return nil, f.machRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, oerr.msg, nil,
			[]*typev1.FieldViolation{{Field: "order_by", Description: oerr.msg}}))
	}
	if order == nil {
		order = defaultMachineOrder
	}
	size := int(m.GetPageSize())
	if size <= 0 {
		size = 50
	}
	if size > 500 {
		size = 500
	}
	now := f.hubNow()
	rows, err := statedb.ListMachineRows(ctx, f.store)
	if err != nil {
		return nil, f.machFailed(ctx, actor, "store", err)
	}
	matched := rows[:0]
	for _, r := range rows {
		if nodeMatchesMachine(node, r) {
			matched = append(matched, r)
		}
	}
	sortMachineRows(matched, order)
	want := machineQueryHash(m.GetFilter(), m.GetOrderBy(), size)
	start := 0
	if m.GetPageToken() != "" {
		cursor, err := parseMachineToken(m.GetPageToken(), want, len(order), now)
		if err != nil {
			return nil, f.machRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID, "bad page_token", nil,
				[]*typev1.FieldViolation{{Field: "page_token", Description: "bad page_token"}}))
		}
		start = sort.Search(len(matched), func(i int) bool {
			return machineKeyAfter(matched[i], order, cursor.key, cursor.id)
		})
	}
	end := min(start+size, len(matched))
	page := matched[start:end]
	out := make([]*hubv1.Machine, 0, len(page))
	for _, r := range page {
		pm, err := machineProto(r)
		if err != nil {
			return nil, f.machFailed(ctx, actor, "store", err)
		}
		out = append(out, pm)
	}
	next := ""
	if end < len(matched) {
		last := matched[end-1]
		next = mintMachineToken(want, machineSortKey(last, order), last.MachineID, now)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.ListMachinesResponse{Machines: out, NextPageToken: next}), nil
}

// GetMachine reads one enrolled machine by id.
func (f *Fleet) GetMachine(ctx context.Context, req *connect.Request[hubv1.GetMachineRequest],
) (*connect.Response[hubv1.GetMachineResponse], error) {
	who, err := authorizeMachines(ctx)
	if err != nil {
		return nil, f.machRefused(ctx, err)
	}
	actor := who.GetId().GetValue()
	id := req.Msg.GetId().GetValue()
	if id == "" {
		return nil, f.machRefused(ctx, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "machine id is required", nil,
			[]*typev1.FieldViolation{{Field: "id", Description: "machine id is required"}}))
	}
	row, err := statedb.ReadMachineRow(ctx, f.store, id)
	if err != nil {
		if errors.Is(err, statedb.ErrMachineUnknown) {
			return nil, f.machRefused(ctx, apiError(connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND, "unknown machine", nil, nil))
		}
		return nil, f.machFailed(ctx, actor, "store", err)
	}
	pm, err := machineProto(row)
	if err != nil {
		return nil, f.machFailed(ctx, actor, "store", err)
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hubv1.GetMachineResponse{Machine: pm}), nil
}

// nodeMatchesMachine evaluates a parsed filter against one stored row. A nil
// node is no filter and matches everything.
func machineState(state string) (hubv1.MachineState, error) {
	switch state {
	case "ENROLLED":
		return hubv1.MachineState_MACHINE_STATE_ENROLLED, nil
	case "ONLINE":
		return hubv1.MachineState_MACHINE_STATE_ONLINE, nil
	case "STALE":
		return hubv1.MachineState_MACHINE_STATE_STALE, nil
	case "OFFLINE":
		return hubv1.MachineState_MACHINE_STATE_OFFLINE, nil
	case "REVOKED":
		return hubv1.MachineState_MACHINE_STATE_REVOKED, nil
	}
	return hubv1.MachineState_MACHINE_STATE_UNSPECIFIED, fmt.Errorf("api: unknown machine state %q", state)
}

// machineProto assembles the Machines view of one stored row (02k: exactly
// what is served). The spec is the enroll-time record; the status is the
// stored state, facts, policy row, resource sample and event counters.
// Unserved so far: the drain reason, by and time (DrainMachine is row 24),
// the local label additions (status.labels is the hub-assigned set), and
// what no row stores yet (harnesses, repos, effective settings with their
// capped, rejected and unenforced sets, the heartbeat health past the event
// counters, spool, battery, clock skew, slots, forge state, certificates,
// capabilities, unfit). A row whose facts or resources do not parse is the
// hub's failure, INTERNAL.
func machineProto(row statedb.MachineRow) (*hubv1.Machine, error) {
	var facts agentv1.MachineFacts
	if err := protojson.Unmarshal([]byte(row.Facts), &facts); err != nil {
		return nil, fmt.Errorf("api: machine facts: %w", err)
	}
	state, err := machineState(row.State)
	if err != nil {
		return nil, err
	}
	created := time.UnixMilli(row.CreatedMillis)
	observed := time.UnixMilli(row.ObservedMillis)
	return machineProtoStatus(row, state, &facts, created, observed)
}

func machineProtoStatus(row statedb.MachineRow, state hubv1.MachineState, facts *agentv1.MachineFacts,
	created, observed time.Time,
) (*hubv1.Machine, error) {
	//nolint:gosec // G115: hub_seq has CHECK >= 0
	status := &hubv1.MachineStatus{
		ObservedTime: timestamppb.New(observed), HubSeq: uint64(row.HubSeq), State: state,
		Hostname: facts.GetHostname(), Os: facts.GetOs(), OsVersion: facts.GetOsVersion(),
		Arch: facts.GetArch(), CpuCount: facts.GetCpuCount(), RamBytes: facts.GetRamBytes(),
		Timezone: facts.GetTimezone(), Username: facts.GetUsername(),
		AgentVersion: facts.GetAgentVersion(), TmuxVersion: facts.GetTmuxVersion(),
		Container: facts.GetContainer(), Labels: row.Labels,
		Draining: row.Drained || row.AgentDraining, InstallId: row.InstallID,
		PolicyHash: row.PolicyHash, PolicyChangedSinceLoad: row.PolicyChanged,
		PolicySafeMode: row.PolicySafe, PolicyError: row.PolicyError,
	}
	tel, err := machineTelemetry(row)
	if err != nil {
		return nil, err
	}
	status.Telemetry = tel
	//nolint:gosec // G115: version has CHECK >= 1
	return &hubv1.Machine{
		Id: &typev1.MachineId{Value: row.MachineID}, Version: uint64(row.Version),
		CreateTime: timestamppb.New(created), UpdateTime: timestamppb.New(created),
		Spec: &hubv1.MachineSpec{
			DisplayName: row.Name, Labels: row.Labels,
			Drain: &hubv1.MachineDrain{Draining: row.Drained},
		},
		Status: status,
	}, nil
}

// machineTelemetry builds the volatile telemetry of one stored row: the
// last-seen time, the last resource sample, and the Hello event counters.
// Nil when the machine never connected and sent no sample. A sample the hub
// cannot parse is the hub's failure, INTERNAL.
func machineTelemetry(row statedb.MachineRow) (*hubv1.MachineTelemetry, error) {
	if row.LastSeenMillis == 0 && row.Resources == "" {
		return nil, nil
	}
	tel := &hubv1.MachineTelemetry{}
	if row.LastSeenMillis > 0 {
		tel.LastSeenTime = timestamppb.New(time.UnixMilli(row.LastSeenMillis))
	}
	if row.Resources != "" {
		var res agentv1.ResourceSample
		if err := protojson.Unmarshal([]byte(row.Resources), &res); err != nil {
			return nil, fmt.Errorf("api: machine resources: %w", err)
		}
		if res.Load1 != nil {
			tel.Load1 = res.GetLoad1()
		}
		if res.Load5 != nil {
			tel.Load5 = res.GetLoad5()
		}
		if res.MemAvailableBytes != nil {
			tel.MemAvailableBytes = res.GetMemAvailableBytes()
		}
		if res.MemPressureLevel != nil {
			tel.MemoryPressureLevel = res.GetMemPressureLevel()
		}
		for _, d := range res.GetDisk() {
			tel.Disk = append(tel.Disk, &hubv1.MachineDisk{
				Path: d.GetPath(), FreeBytes: d.GetFreeBytes(), TotalBytes: d.GetTotalBytes(),
			})
		}
	}
	//nolint:gosec // G115: the counters columns have CHECK >= 0
	tel.AgentHealth = &hubv1.MachineAgentHealth{
		EventsShedTotal: uint64(row.Shed), EventsCoalescedTotal: uint64(row.Coalesced),
		EventsEvictedTotal: uint64(row.Evicted),
	}
	return tel, nil
}

// machRefused logs a machine-read caller error and returns it.
func (f *Fleet) machRefused(ctx context.Context, err error) error {
	f.tel.Logger.InfoContext(ctx, "machines.issue_refused", "reason", reasonOf(err))
	return err
}

// machFailed logs the hub's own machine-read failure and returns INTERNAL
// without the cause's text.
func (f *Fleet) machFailed(ctx context.Context, actor, step string, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "machines.failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not read its machines", nil, nil)
}
