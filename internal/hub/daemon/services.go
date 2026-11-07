package daemon

import (
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/attach"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
	"github.com/linuxdynasty/fleet-board/internal/hub/hookreconcile"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
	"github.com/linuxdynasty/fleet-board/internal/hub/observed"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
)

// services builds what bind listens on: the agent gateway, the Fleet API
// and the quotas. One attach registry pairs the Fleet handlers
// (CreateAttachTicket, AttachTerminal) with the agent gateway (Terminal).
func services(now func() time.Time, cfg *config.Config, opts Options, d hubDeps, sysLoop *system.Loop, s *session) (*gateway.Server, *api.Fleet, *api.Quotas, error) {
	att := attach.New(now, nil)
	agent, err := gateway.NewServer(gateway.ServerOptions{
		Certs: d.certs, Store: d.st, Identity: d.id, Revoked: d.revoked, Telemetry: s.tel, Now: opts.Now,
		Appliers:      []ingest.Applier{observed.Applier{Now: now}, hookreconcile.Applier{Now: now, Expected: hookreconcile.ExpectedVersions()}},
		EventAppliers: []ingest.EventApplier{observed.Applier{Now: now}},
		Attach:        att,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	fleet, err := api.NewFleet(api.FleetOptions{
		Store: d.st, CACert: d.id.CACert, Telemetry: s.tel, Now: opts.Now,
		StateDir: cfg.StateDir, LocalTrusted: cfg.Mode == "local_trusted",
		Revoker: d.revoked, ByeMachine: agent.ByeMachine, OnCommandsClosed: agent.Outbox().Closed,
		OnCommandQueued: func(op outbox.Op) { agent.Outbox().Notify(op) },
		HubInstanceID:   agent.InstanceID(), SystemLoop: sysLoop,
		Attach: att, MachineLive: agent.Live,
		HubHostname: d.hostname,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	quotas, err := api.NewQuotas(cfg.RateLimits, s.tel, opts.Now)
	if err != nil {
		return nil, nil, nil, err
	}
	return agent, fleet, quotas, nil
}
