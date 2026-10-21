package daemon_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
)

// openWatch opens a Watch stream on the running hub's local API with one
// subscription per topic, all with send_initial.
func openWatch(t *testing.T, r *running, topics ...hubv1.WatchTopic,
) (*connect.ServerStreamForClient[hubv1.WatchResponse], context.CancelFunc) {
	t.Helper()
	client := hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+r.addrs.API.String())
	subs := make([]*hubv1.TopicSubscription, 0, len(topics))
	for _, topic := range topics {
		subs = append(subs, &hubv1.TopicSubscription{Topic: topic, SendInitial: true})
	}
	ctx, stop := context.WithCancel(t.Context())
	stream, err := client.Watch(ctx, connect.NewRequest(&hubv1.WatchRequest{Subscriptions: subs}))
	if err != nil {
		stop()
		t.Fatalf("Watch = %v, want a served stream", err)
	}
	return stream, stop
}

// nextWatch reads one message within the timeout.
func nextWatch(t *testing.T, stream *connect.ServerStreamForClient[hubv1.WatchResponse],
) *hubv1.WatchResponse {
	t.Helper()
	got := make(chan *hubv1.WatchResponse, 1)
	go func() {
		defer close(got)
		if stream.Receive() {
			got <- stream.Msg()
		}
	}()
	select {
	case m, ok := <-got:
		if !ok {
			t.Fatalf("Watch closed: %v", stream.Err())
		}
		return m
	case <-time.After(10 * time.Second):
		t.Fatal("no Watch message within 10s")
		return nil
	}
}

// drainInitial reads until every topic reports INITIAL_DONE.
func drainInitial(t *testing.T, stream *connect.ServerStreamForClient[hubv1.WatchResponse], topics ...hubv1.WatchTopic) {
	t.Helper()
	done := map[hubv1.WatchTopic]bool{}
	for len(done) < len(topics) {
		m := nextWatch(t, stream)
		switch m.GetChangeType() {
		case hubv1.ChangeType_CHANGE_TYPE_INITIAL_DONE:
			done[m.GetTopic()] = true
		case hubv1.ChangeType_CHANGE_TYPE_INITIAL:
		default:
			t.Fatalf("drain got %v %v, want initial state", m.GetChangeType(), m.GetTopic())
		}
	}
}

// An enroll commit on the agent listener reaches a Watch subscriber: the
// daemon wires the gateway's WatchSink to the Fleet in serve.
func TestRunPublishesGatewayEnrollToWatch(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	r := h.start(t, h.options())
	defer r.stop(t)
	stream, stop := openWatch(t, r, hubv1.WatchTopic_WATCH_TOPIC_MACHINES)
	defer stop()
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_MACHINES)
	enrollOverRun(t, h, r)
	m := nextWatch(t, stream)
	if m.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_MACHINES ||
		m.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED || m.GetMachine() == nil {
		t.Errorf("live = %v %v machine=%v, want MACHINES CREATED with the enrolled machine",
			m.GetChangeType(), m.GetTopic(), m.GetMachine() != nil)
	}
}

// The housekeeping watch_publish job tails audit rows that no commitWatch
// covers: the gateway enroll job writes its machine.enroll audit row
// through the store writer, and it goes live only after a driven tick.
func TestRunWatchPublishTickTailsAudit(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	h.init(t)
	opts := h.options()
	ticks := make(chan time.Time)
	opts.HousekeepingTicks = ticks
	r := h.start(t, opts)
	defer r.stop(t)
	stream, stop := openWatch(t, r, hubv1.WatchTopic_WATCH_TOPIC_AUDIT)
	defer stop()
	drainInitial(t, stream, hubv1.WatchTopic_WATCH_TOPIC_AUDIT)
	enrollOverRun(t, h, r)
	ticks <- time.Time{}
	ticks <- time.Time{} // the second send returns once the first check finished
	m := nextWatch(t, stream)
	if m.GetTopic() != hubv1.WatchTopic_WATCH_TOPIC_AUDIT ||
		m.GetChangeType() != hubv1.ChangeType_CHANGE_TYPE_CREATED || m.GetAuditEntry() == nil {
		t.Errorf("live = %v %v audit=%v, want AUDIT CREATED with the enroll entry",
			m.GetChangeType(), m.GetTopic(), m.GetAuditEntry() != nil)
	}
}
