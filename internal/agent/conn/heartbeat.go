package conn

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

const (
	// DefaultHeartbeat is the A5 heartbeat interval (defaults.md
	// "Heartbeat"), used when HelloAck.heartbeat_interval is not 1 to 15 s.
	DefaultHeartbeat = 15 * time.Second
	minHeartbeat     = time.Second
)

// started is when this process started; Heartbeat.uptime is measured from
// it on the monotonic clock, so a wall-clock step does not move it.
var started = time.Now()

// heartbeatInterval is HelloAck.heartbeat_interval when it is 1 to 15 s,
// else DefaultHeartbeat.
func heartbeatInterval(d *durationpb.Duration) time.Duration {
	if d == nil || d.CheckValid() != nil {
		return DefaultHeartbeat
	}
	if v := d.AsDuration(); v >= minHeartbeat && v <= DefaultHeartbeat {
		return v
	}
	return DefaultHeartbeat
}

// sendHeartbeat sends one A5 Heartbeat on Run's goroutine, the stream's
// only sender, so it never lands between a snapshot and its results. It is
// SHED and carries no seq (02i): a tick missed while a send waits is
// dropped by the ticker, never queued. The hook fills what later rows own;
// the four fields this row owns are set after it, so it cannot change them.
// A failed agent.db read or send closes the stream (step "heartbeat").
func (s *session) sendHeartbeat(ctx context.Context) error {
	l := s.l
	st, err := l.opts.DB.State(ctx)
	if err != nil {
		return err
	}
	hb := &agentv1.Heartbeat{}
	if hook := l.opts.Heartbeat; hook != nil {
		hook(ctx, hb)
	}
	hb.SentTime = timestamppb.Now()
	hb.Uptime = durationpb.New(time.Since(started))
	hb.EventSeq = l.seqs.Last()
	hb.Events = &agentv1.EventCounters{
		ShedTotal: st.Marks.Shed, CoalescedTotal: st.Marks.Coalesced, EvictedTotal: st.Marks.Evicted,
	}
	return s.send(&agentv1.ConnectRequest{Body: &agentv1.ConnectRequest_Heartbeat{Heartbeat: hb}})
}
