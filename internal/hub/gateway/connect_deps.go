package gateway

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// connectDeps is what Connect needs beyond enrollDeps, built once per
// listener process by NewServer.
type connectDeps struct {
	streams *registry
	limit   *helloLimit
	revoked RevokedSet
	tracer  trace.Tracer
	// instanceID is HelloAck.hub_instance_id: random per process start.
	instanceID string
	version    string
	helloWait  time.Duration
	arms       armTable
	m          connectMetrics
}

type connectMetrics struct {
	reconnects, duplicates, unhandled, refused metric.Int64Counter
	epoch                                      metric.Int64Gauge
}

func newConnectDeps(opts ServerOptions) (*connectDeps, error) {
	var b [16]byte // from crypto/rand, not opts.Random: an id, not a secret or a serial tests count
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return nil, fmt.Errorf("gateway: hub instance id: %w", err)
	}
	m := opts.Telemetry.Meter(scope)
	p := opts.Telemetry.MetricPrefix()
	var cm connectMetrics
	var errs [5]error
	cm.reconnects, errs[0] = m.Int64Counter(p+"_reconnects_total",
		metric.WithDescription("Hellos the hub accepted, by machine and Hello.reason (start, reconnect, wake, network_change, unspecified)."))
	cm.epoch, errs[1] = m.Int64Gauge(p+"_epoch",
		metric.WithDescription("The connection epoch of each machine's last accepted Hello."))
	cm.duplicates, errs[2] = m.Int64Counter(p+"_duplicate_identity_total",
		metric.WithDescription("Live streams a newer Hello for the same machine displaced (Bye{DUPLICATE_IDENTITY})."))
	cm.unhandled, errs[3] = m.Int64Counter(p+"_connect_unhandled_total",
		metric.WithDescription("Connect messages of an arm the hub has no handler for yet, by kind; ignored."))
	cm.refused, errs[4] = m.Int64Counter(p+"_connect_refused_total",
		metric.WithDescription("Hellos the hub refused before HelloAck, by cause (02g \"Hello refusals\")."))
	if err := errors.Join(errs[:]...); err != nil {
		return nil, fmt.Errorf("gateway: connect metrics: %w", err)
	}
	return &connectDeps{
		streams: newRegistry(), limit: newHelloLimit(), revoked: opts.Revoked,
		tracer: opts.Telemetry.Tracer(scope), instanceID: hex.EncodeToString(b[:]), version: buildinfo.Version,
		helloWait: cmp.Or(opts.HelloWait, HelloWait), arms: maps.Clone(arms), m: cm,
	}, nil
}

// accepted logs reconnect.hello and records the epoch and the reconnect.
func (a *agentService) accepted(ctx context.Context, s *stream, h *agentv1.Hello, out statedb.HelloOut) {
	machine := attribute.String("machine", s.machineID)
	reason := string(connectReason(h.GetReason()))
	a.m.reconnects.Add(ctx, 1, metric.WithAttributes(machine, attribute.String("reason", reason)))
	a.m.epoch.Record(ctx, int64(out.Epoch), metric.WithAttributes(machine)) //nolint:gosec // G115: epoch <= statedb.MaxEpoch
	a.log.InfoContext(ctx, "reconnect.hello", "machine_id", s.machineID, "epoch", out.Epoch, "last_epoch", h.GetLastEpoch(),
		"last_event_seq", h.GetLastEventSeq(), "hub_last_event_seq", out.HubLastEventSeq, "reason", reason, "peer", s.peer)
}

// duplicate logs identity.duplicate and counts a live stream (old) that
// a newer Hello (now) displaced; old gets Bye{DUPLICATE_IDENTITY}. A row a
// hub crash left live, which no stream holds, is never counted.
func (a *agentService) duplicate(ctx context.Context, old, now *stream) {
	a.m.duplicates.Add(ctx, 1, metric.WithAttributes(attribute.String("machine", now.machineID)))
	age := int64(-1)
	if hb := old.lastHeartbeat(); !hb.IsZero() {
		age = a.now().Sub(hb).Milliseconds()
	}
	a.log.InfoContext(ctx, "identity.duplicate", "machine_id", now.machineID, "old_epoch", old.epoch, "new_epoch", now.epoch,
		"old_boot_id", old.bootID, "new_boot_id", now.bootID, "old_peer", old.peer, "new_peer", now.peer,
		"heartbeat_age_ms", age)
}

func byeLabel(r agentv1.ByeReason) string { return strings.TrimPrefix(r.String(), "BYE_REASON_") }
