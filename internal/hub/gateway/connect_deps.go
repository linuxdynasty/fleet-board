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
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/hub/ingest"
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
	afterHello func()
	// afterRegister, set only by tests, runs once the stream is the
	// machine's live one, before its HelloAck is queued.
	afterRegister func()
	// beforeRowClose, set only by tests, runs before a stream's row close.
	beforeRowClose func()
	// afterHeartbeat, set only by tests, runs after the handler goroutine
	// finished with a Heartbeat wake.
	afterHeartbeat func(n uint64)
	arms           armTable
	// started is the hub clock when this listener process was built: the
	// silence of a machine with no live stream is counted from it (03n
	// "Hub start").
	started time.Time
	skew    *skews
	sm      stateMetrics
	// stopping ends when Shutdown begins (beginShutdown): a stream still
	// waiting for its Hello or for its Hello job stops waiting.
	stopping context.Context
	stop     context.CancelFunc
	m        connectMetrics
	// The Snapshot arm (snapshot.go): its metrics, the appliers, the
	// after-commit hook, and a hook tests run before the job.
	snap           snapshotMetrics
	appliers       []ingest.Applier
	onApplied      func(machineID string, epoch uint64)
	beforeSnapshot func(context.Context)
}

type connectMetrics struct {
	reconnects, duplicates, unhandled, refused, beforeHello metric.Int64Counter
	epoch                                                   metric.Int64Gauge
}

func newConnectDeps(opts ServerOptions) (*connectDeps, error) {
	var b [16]byte // from crypto/rand, not opts.Random: an id, not a secret or a serial tests count
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return nil, fmt.Errorf("gateway: hub instance id: %w", err)
	}
	m := opts.Telemetry.Meter(scope)
	p := opts.Telemetry.MetricPrefix()
	var cm connectMetrics
	var errs [6]error
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
	cm.beforeHello, errs[5] = m.Int64Counter(p+"_connect_closed_before_hello_total",
		metric.WithDescription("Connect streams with a certificate that ended before a Hello was accepted, by err_kind: "+
			"oversize, reset, timeout (the Hello wait) or shutdown."))
	if err := errors.Join(errs[:]...); err != nil {
		return nil, fmt.Errorf("gateway: connect metrics: %w", err)
	}
	sm, err := newStateMetrics(m, p)
	if err != nil {
		return nil, err
	}
	snap, err := newSnapshotMetrics(opts.Telemetry)
	if err != nil {
		return nil, err
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	stopping, stop := context.WithCancel(context.Background())
	return &connectDeps{
		afterHeartbeat: opts.afterHeartbeat, beforeRowClose: opts.beforeRowClose, started: now().UTC(), skew: newSkews(), sm: sm,
		snap: snap, appliers: slices.Clone(opts.Appliers), onApplied: opts.OnSnapshotApplied, beforeSnapshot: opts.beforeSnapshot,
		stopping: stopping, stop: stop,
		streams: newRegistry(), limit: newHelloLimit(), revoked: opts.Revoked,
		tracer: opts.Telemetry.Tracer(scope), instanceID: hex.EncodeToString(b[:]), version: buildinfo.Version,
		helloWait: cmp.Or(opts.HelloWait, HelloWait), afterHello: opts.afterHello, afterRegister: opts.afterRegister, arms: withArms(arms, opts.arms), m: cm,
	}, nil
}

// withArms is a copy of the product table with extra (tests) added.
func withArms(product, extra armTable) armTable {
	t := maps.Clone(product)
	maps.Copy(t, extra)
	return t
}

// beginShutdown is the shutdown hook (http.Server.RegisterOnShutdown):
// streams waiting for a Hello or a Hello job stop waiting, and every live
// stream ends with Bye{HUB_RESTART}.
func (d *connectDeps) beginShutdown() {
	d.stop()
	d.streams.byeAll()
}

// endedBeforeHello logs reconnect.closed for a stream with a certificate
// that ended before a Hello was accepted (epoch 0, no row), with err_kind
// oversize, reset, timeout or shutdown, and counts it.
func (a *agentService) endedBeforeHello(ctx context.Context, machineID, kind string) {
	a.m.beforeHello.Add(ctx, 1, metric.WithAttributes(attribute.String("err_kind", kind)))
	a.log.InfoContext(ctx, "reconnect.closed", "machine_id", machineID, "epoch", 0, "reason", "before_hello",
		"err_kind", kind, "row_closed", false)
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
