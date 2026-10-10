package typing

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/agent/tmux/typing"

// Recorder counts and logs refused lines. Make one with NewRecorder and pass
// it to every constructor; a nil *Recorder counts and logs nowhere, which is
// for tests of the caller.
type Recorder struct {
	log     *slog.Logger
	refusal metric.Int64Counter
}

// NewRecorder returns a Recorder that logs through tel.Logger and counts in
// tel's meter. A nil tel makes one that logs and counts nowhere.
func NewRecorder(tel *telemetry.Telemetry) (*Recorder, error) {
	log := slog.New(slog.DiscardHandler)
	m, prefix := noop.NewMeterProvider().Meter(scope), "fleet_agent"
	if tel != nil {
		log, m, prefix = tel.Logger.With("component", "typing"), tel.Meter(scope), tel.MetricPrefix()
	}
	c, err := m.Int64Counter(prefix+"_typing_refused_total",
		metric.WithDescription("send-keys lines the typing package refused to build, by reason."))
	if err != nil {
		return nil, err
	}
	return &Recorder{log: log, refusal: c}, nil
}

// Sent logs typing.sent for a line the tmux client sent and tmux accepted
// (plan row 7 PR 7c2 calls it): the purpose, the server kind, the pane id and
// the bytes of literal text (0 for a line of named keys). Ids and counts
// only, never the text. A nil Recorder or the zero Line logs nothing.
func (r *Recorder) Sent(ctx context.Context, l Line) {
	if r == nil || l.IsZero() {
		return
	}
	r.log.InfoContext(ctx, "typing.sent",
		"purpose", l.purpose.String(),
		"server_kind", serverKinds[l.server],
		"pane_id", l.paneID,
		"bytes", l.text)
}

// refused counts and logs one refusal and returns it as an error. The line
// has the reason and the stated purpose: never the text, never the pane
// (a refused pane is caller input too).
func (r *Recorder) refused(err *RefusedError, purpose Purpose) error {
	if r != nil {
		ctx := context.Background()
		r.refusal.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", string(err.Reason))))
		r.log.WarnContext(ctx, "typing.refused", "reason", string(err.Reason), "purpose", purpose.String())
	}
	return err
}
