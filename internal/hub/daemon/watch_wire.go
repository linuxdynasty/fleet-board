package daemon

import (
	"context"

	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
)

// wireWatchSink hands the gateway's agent writes to Watch subscribers
// through the Fleet (plan row 12, slice 2): snapshot and event commits,
// presence changes and enrollments publish after they land, serialized on
// the Fleet's watchMu with the in-package writes. Unset, the gateway
// publishes nothing. It also appends the dispatch row's registry pass to
// the ingest jobs, so agent sightings drive dispatched goals (row 22).
func wireWatchSink(agent *gateway.Server, fleet *api.Fleet) {
	reg := api.NewRegistryApplier(fleet)
	agent.AddAppliers([]ingest.Applier{reg}, []ingest.EventApplier{reg})
	agent.SetWatchSink(gateway.WatchSink{
		MachineState:       fleet.PublishMachineState,
		MachineCreated:     fleet.PublishMachineCreated,
		MachineCommitted:   fleet.PublishMachineCommitted,
		MachineEvent:       fleet.PublishMachineEvent,
		GoalsCommitted:     fleet.PublishGoalsCommitted,
		WorkItemsCommitted: fleet.PublishWorkItems,
		Pane: func(ctx context.Context, p gateway.PanePublication) {
			fleet.PublishPane(ctx, api.PanePublication{
				Pane: p.Pane, MachineID: p.MachineID, SessionID: p.SessionID,
				LastLines: p.LastLines, LastOutputTime: p.LastOutputTime,
				Activity: p.Activity, Seq: p.Seq,
			})
		},
	})
}
