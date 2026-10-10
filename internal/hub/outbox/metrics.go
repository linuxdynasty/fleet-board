package outbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// metrics are the command path's instruments (03 "Telemetry naming", 03f
// "Command path telemetry", 05b). Labels are closed: kind is a Command arm
// name, status a Status name, arm a ConnectRequest arm, outcome and result
// the fixed sets below, field the free-text field, machine a machine id.
type metrics struct {
	commands, messages, unknown, sends, scrubbed, oversize metric.Int64Counter
	roundtrip                                              metric.Float64Histogram
	reg                                                    metric.Registration
}

// pendingFunc reads the pending rows per machine: count and oldest create
// time.
type pendingFunc func(ctx context.Context) (map[string]pendingStat, error)

type pendingStat struct {
	n      int64
	oldest time.Time
}

func newMetrics(tel *telemetry.Telemetry, pending pendingFunc, clock func() time.Time) (*metrics, error) {
	m := tel.Meter(scope)
	p := tel.MetricPrefix()
	var x metrics
	var errs [9]error
	x.commands, errs[0] = m.Int64Counter(p+"_commands_total",
		metric.WithDescription("Commands that ended, by kind and final status (a CommandResult the hub stored)."))
	x.roundtrip, errs[1] = m.Float64Histogram(p+"_command_roundtrip_seconds", metric.WithUnit("s"),
		metric.WithDescription("Time from a command's enqueue to its stored CommandResult, by kind."))
	x.messages, errs[2] = m.Int64Counter(p+"_command_messages_total",
		metric.WithDescription("CommandAck, CommandProgress and CommandResult messages by arm and outcome: applied, "+
			"known (a repeat), unknown_command, wrong_machine, stale_stream, invalid, epoch_mismatch and seq_replayed "+
			"(a refusal the agent did not log), failed (store error)."))
	x.unknown, errs[3] = m.Int64Counter(p+"_results_unknown_command_total",
		metric.WithDescription("CommandResults for a command_id the hub has no row for; refused, never stored."))
	x.sends, errs[4] = m.Int64Counter(p+"_outbox_sends_total",
		metric.WithDescription("Command sends by result: queued, full (the stream's queue was full; retried), "+
			"not_live (the epoch's stream is gone), invalid (the stored command does not decode; skipped)."))
	x.scrubbed, errs[5] = m.Int64Counter(p+"_scrub_total",
		metric.WithDescription("Agent free text the hub scrubbed before storing it, by field and result: clean, "+
			"redacted, unsettled (replaced whole)."))
	x.oversize, errs[6] = m.Int64Counter(p+"_scrub_oversize_total",
		metric.WithDescription("Agent free text over its field's cap, cut to the cap before the scrub ran, by field."))
	count, errPending := m.Int64ObservableGauge(p+"_outbox_pending",
		metric.WithDescription("Commands pending (not yet acknowledged) per machine."))
	age, errAge := m.Float64ObservableGauge(p+"_outbox_oldest_age_seconds", metric.WithUnit("s"),
		metric.WithDescription("Age of each machine's oldest pending command."))
	errs[7], errs[8] = errPending, errAge
	if err := errors.Join(errs[:]...); err != nil {
		return nil, fmt.Errorf("outbox: metrics: %w", err)
	}
	reg, err := m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		stats, err := pending(ctx)
		if err != nil {
			return nil // a closed or busy store reports nothing this scrape
		}
		now := clock()
		for machine, st := range stats {
			attrs := metric.WithAttributes(attribute.String("machine", machine))
			o.ObserveInt64(count, st.n, attrs)
			o.ObserveFloat64(age, max(0, now.Sub(st.oldest).Seconds()), attrs)
		}
		return nil
	}, count, age)
	if err != nil {
		return nil, fmt.Errorf("outbox: metrics: %w", err)
	}
	x.reg = reg
	return &x, nil
}

func (x *metrics) close() { _ = x.reg.Unregister() }

func (x *metrics) message(ctx context.Context, arm, outcome string) {
	x.messages.Add(ctx, 1, metric.WithAttributes(attribute.String("arm", arm), attribute.String("outcome", outcome)))
}

func (x *metrics) send(ctx context.Context, r SendResult) {
	x.sends.Add(ctx, 1, metric.WithAttributes(attribute.String("result", sendLabel[r])))
}

var sendLabel = map[SendResult]string{SendQueued: "queued", SendFull: "full", SendNotLive: "not_live"}

// sendInvalid counts a stored command that did not decode.
func (x *metrics) sendInvalid(ctx context.Context) {
	x.sends.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "invalid")))
}

// text counts one cleaned free-text field.
func (x *metrics) text(ctx context.Context, field string, c cleaned) {
	f := attribute.String("field", field)
	if c.oversize {
		x.oversize.Add(ctx, 1, metric.WithAttributes(f))
	}
	result := "clean"
	switch {
	case c.unsettled:
		result = "unsettled"
	case c.redacted:
		result = "redacted"
	}
	x.scrubbed.Add(ctx, 1, metric.WithAttributes(f, attribute.String("result", result)))
}
