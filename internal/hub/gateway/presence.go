package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Machine state timing (defaults.md "Machines and the agent connection").
const (
	// StaleAfter: no Heartbeat for this long is STALE, and a reader stalled
	// this long is cut at STALE.
	StaleAfter = 45 * time.Second
	// OfflineAfter: no Heartbeat for this long is OFFLINE.
	OfflineAfter = 3 * time.Minute
	// LastSeenEvery: last_seen_time is written from Heartbeats at most this
	// often per stream (and on connect and disconnect).
	LastSeenEvery = time.Minute
)

// machineStates are the stored states fleet_hub_machine_state reports.
var machineStates = []string{
	statedb.MachineEnrolled, statedb.MachineOnline, statedb.MachineStale, statedb.MachineOffline, statedb.MachineRevoked,
}

type stateMetrics struct {
	state  metric.Int64Gauge
	skew   metric.Float64Gauge
	closed metric.Int64Counter
}

func newStateMetrics(m metric.Meter, p string) (stateMetrics, error) {
	var sm stateMetrics
	var errs [3]error
	sm.state, errs[0] = m.Int64Gauge(p+"_machine_state",
		metric.WithDescription("1 for each machine's stored state, 0 for its other states (ENROLLED, ONLINE, STALE, OFFLINE, REVOKED)."))
	sm.skew, errs[1] = m.Float64Gauge(p+"_clock_skew_seconds", metric.WithUnit("s"),
		metric.WithDescription("Agent clock minus hub clock per machine: the median of its last five Hello and Heartbeat samples."))
	sm.closed, errs[2] = m.Int64Counter(p+"_stream_closed_total",
		metric.WithDescription("Connect streams housekeeping cut, by reason: ingest_stalled (at STALE) or offline."))
	if err := errors.Join(errs[:]...); err != nil {
		return stateMetrics{}, fmt.Errorf("gateway: machine state metrics: %w", err)
	}
	return sm, nil
}

// Housekeep is the machine-state job of fleetd's housekeeping (03f, 03n): it
// reads every machine and its live connection on the read pool and, for an
// ONLINE or STALE one, the silence since the last thing heard (the newest
// of this process's start, the live row's Hello, the live stream's last
// applied Snapshot and its last Heartbeat, by the hub clock):
//
//   - 3 minutes or more: OFFLINE, the live row closed as offline in the
//     same job, then the live stream cut (offline);
//   - 45 s or more while ONLINE: STALE; the live stream may then come back
//     with a Heartbeat;
//   - while STALE, a live stream whose reader has been stalled 45 s or more
//     is cut (ingest_stalled); its row's close takes the machine OFFLINE.
//
// Each change is one writer job that holds only while the state and the
// live epoch are still what was read. It also sets fleet_hub_machine_state
// for every machine. Errors are joined; the other machines are still done.
func (s *Server) Housekeep(ctx context.Context, now time.Time) error {
	a := &agentService{enrollDeps: s.deps, connectDeps: s.connect}
	list, err := statedb.MachineStates(ctx, a.store)
	if err != nil {
		return err
	}
	var errs []error
	for _, m := range list {
		a.recordState(ctx, m.MachineID, m.State)
		if m.State == statedb.MachineOnline || m.State == statedb.MachineStale {
			errs = append(errs, a.judge(ctx, m, now))
		}
	}
	return errors.Join(errs...)
}

// judge decides one ONLINE or STALE machine.
func (a *agentService) judge(ctx context.Context, m statedb.MachineLive, now time.Time) error {
	s := a.streams.get(m.MachineID)
	if s != nil && s.epoch != m.Epoch {
		s = nil // a stream the store no longer has live, or one not registered yet
	}
	heard := a.started
	if m.Epoch != 0 && m.Started.After(heard) {
		heard = m.Started
	}
	if s != nil {
		for _, t := range []time.Time{s.lastSnapshot(), s.lastHeartbeat()} {
			if t.After(heard) {
				heard = t
			}
		}
	}
	silence := now.Sub(heard)
	switch {
	case silence >= OfflineAfter:
		c, err := statedb.MarkState(ctx, a.store, statedb.Mark{MachineID: m.MachineID, Epoch: m.Epoch, To: statedb.MachineOffline, Now: now})
		if err != nil || c == nil {
			return err
		}
		a.stateChanged(ctx, *c)
		a.cut(ctx, m.MachineID, m.Epoch, statedb.EndOffline, now)
	case m.State == statedb.MachineOnline && silence >= StaleAfter:
		c, err := statedb.MarkState(ctx, a.store, statedb.Mark{MachineID: m.MachineID, Epoch: m.Epoch, To: statedb.MachineStale, Now: now})
		if err != nil || c == nil {
			return err
		}
		a.stateChanged(ctx, *c)
		if s != nil {
			s.staled.Store(true)
			s.wake() // a Heartbeat that raced the mark brings it back now
			a.cutStalled(ctx, s, now)
		}
	case m.State == statedb.MachineStale && s != nil:
		a.cutStalled(ctx, s, now)
	}
	return nil
}

// cutStalled cuts s when its reader has been stalled for [StaleAfter].
func (a *agentService) cutStalled(ctx context.Context, s *stream, now time.Time) {
	if d := s.stalledFor(now); d >= StaleAfter {
		a.cut(ctx, s.machineID, s.epoch, statedb.EndIngestStalled, now)
	}
}

// cut ends machineID's live stream of epoch, if the registry holds it and
// it is still running, with no Bye (the handler returns nil: the stream
// ends with an OK status; the agent backs off and reconnects) and cancels
// its context, then logs hub.stream_closed and counts it.
func (a *agentService) cut(ctx context.Context, machineID string, epoch uint64, reason statedb.EndReason, now time.Time) {
	s := a.streams.get(machineID)
	if s == nil || s.epoch != epoch {
		return
	}
	// Read before the cut: the end wakes the reader, which clears its stall.
	stalled, depth := s.stalledFor(now), len(s.in)
	if a.streams.cut(machineID, epoch, reason) == nil {
		return
	}
	a.sm.closed.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", string(reason))))
	a.log.WarnContext(ctx, "hub.stream_closed", "machine_id", machineID, "epoch", epoch, "reason", string(reason),
		"stalled_s", int64(stalled/time.Second), "queue_depth", depth)
}

// heartbeat runs on the stream's handler goroutine after a Heartbeat (or a
// STALE mark) woke it: one writer job when the machine must come back from
// STALE or last_seen is due (once a minute), none otherwise.
func (a *agentService) heartbeat(ctx context.Context, s *stream) {
	n := s.beatsRead.Load()
	defer func() {
		if a.afterHeartbeat != nil {
			a.afterHeartbeat(n)
		}
	}()
	select {
	case <-s.ended: // a wake that raced the stream's end (a cut): nothing to write
		return
	default:
	}
	hb := s.lastHeartbeat()
	if hb.IsZero() {
		return
	}
	promote := s.staled.Load() && a.now().Sub(hb) < StaleAfter
	if !promote && hb.Sub(s.seenWritten) < LastSeenEvery {
		return
	}
	// last_seen is the Heartbeat's receive time, never later.
	c, err := statedb.HeartbeatSeen(ctx, a.store, statedb.Beat{MachineID: s.machineID, Epoch: s.epoch, Promote: promote, Now: hb.UTC()})
	if err != nil {
		a.log.ErrorContext(ctx, "connect.failed", "step", "heartbeat", "err_kind", errKind(err), "machine_id", s.machineID,
			"epoch", s.epoch)
		return
	}
	s.seenWritten = hb
	if promote {
		s.staled.Store(false)
	}
	if c != nil {
		a.stateChanged(ctx, *c)
	}
}

// RecordStates sets fleet_hub_machine_state for changes committed before
// this server existed (the start sweep, which logs them itself).
func (s *Server) RecordStates(ctx context.Context, changes []statedb.StateChange) {
	a := &agentService{enrollDeps: s.deps, connectDeps: s.connect}
	for _, c := range changes {
		a.recordState(ctx, c.MachineID, c.To)
	}
}

// stateChanged logs machine.state_changed and moves the state gauge.
func (a *agentService) stateChanged(ctx context.Context, c statedb.StateChange) {
	a.log.InfoContext(ctx, "machine.state_changed", "machine_id", c.MachineID, "from", c.From, "to", c.To, "reason", c.Reason)
	a.recordState(ctx, c.MachineID, c.To)
}

// recordState sets fleet_hub_machine_state{machine, state} to 1 for state
// and 0 for the others.
func (a *agentService) recordState(ctx context.Context, machineID, state string) {
	machine := attribute.String("machine", machineID)
	for _, st := range machineStates {
		v := int64(0)
		if st == state {
			v = 1
		}
		a.sm.state.Record(ctx, v, metric.WithAttributes(machine, attribute.String("state", st)))
	}
}
