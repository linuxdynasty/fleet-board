package api

import (
	"context"
	"sort"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// This file pumps one Watch stream: the initial state, the replay, then
// live (02c). send is stream.Send in production and a stub in unit tests,
// so the slow-consumer policy is proven without a network.

// watchCheckpointInterval sends a CHECKPOINT with a fresh token on an idle
// stream (defaults.md).
const watchCheckpointInterval = 30 * time.Second

// watchSummaryInterval paces FLEET_SUMMARY sends per stream (02c: at most
// one per second).
const watchSummaryInterval = time.Second

// watchSubscription is one validated subscription of a stream.
type watchSubscription struct {
	def         *watchTopicDef
	filter      string
	hash        string
	node        *filterNode
	sendInitial bool
	covered     bool
}

// tokenSub names the subscription in the next resume token.
func (s *watchSubscription) tokenSub() watchTokenSub {
	return watchTokenSub{Topic: s.def.topic, Filter: s.hash}
}

// watchSession pumps one stream: the initial state, the replay, then live.
// send is stream.Send in production and a stub in unit tests, so the
// slow-consumer policy is proven without a network.
type watchSession struct {
	bus        *watchBus
	instanceID string
	store      *store.Store
	subs       []*watchSubscription
	sub        *watchSub
	snap       uint64
	resumeSeq  uint64
	hasResume  bool
	send       func(*hubv1.WatchResponse) error
	now        func() time.Time
	checkpoint time.Duration
	summarize  func(ctx context.Context) (*hubv1.FleetSummary, error)
	lastSeq    uint64
	summaryAt  time.Time
	resyncs    metric.Int64Counter
}

func (s *watchSession) token(seq uint64) string {
	subs := make([]watchTokenSub, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub.tokenSub())
	}
	return mintWatchToken(s.instanceID, seq, subs)
}

// stamp clones msg with its position and a fresh token.
func (s *watchSession) stamp(msg *hubv1.WatchResponse, seq uint64) *hubv1.WatchResponse {
	out, _ := proto.Clone(msg).(*hubv1.WatchResponse)
	if out == nil {
		out = &hubv1.WatchResponse{}
	}
	out.HubSeq = seq
	out.ResumeToken = s.token(seq)
	return out
}

// sendErr maps a send failure: a send that fails because the stream is
// gone reports the context, so the handler can close cleanly.
func sendErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// emit sends one message, tracking the position for the next token.
func (s *watchSession) emit(ctx context.Context, msg *hubv1.WatchResponse, seq uint64) error {
	if seq > s.lastSeq {
		s.lastSeq = seq
	}
	return sendErr(ctx, s.send(s.stamp(msg, seq)))
}

// run sends the initial state and the replay, then pumps live until the
// stream ends. A nil return is a clean close (client cancel or
// RESYNC_REQUIRED sent).
func (s *watchSession) run(ctx context.Context) error {
	if err := s.initial(ctx); err != nil {
		return err
	}
	if err := s.replay(ctx); err != nil {
		return err
	}
	return s.live(ctx)
}

// initial sends the INITIAL state of every send_initial subscription with
// one INITIAL_DONE per topic, plus the current value of every covered
// latest-wins subscription. The bus subscription predates the store read,
// so changes that land in between arrive live and the client dedupes them
// (02c, the dashboard sequence).
func (s *watchSession) initial(ctx context.Context) error {
	for _, sub := range s.subs {
		if !sub.sendInitial && (!sub.covered || !sub.def.latestWins) {
			continue
		}
		// Live-only topics (operations) complete their initial state at
		// once: there is nothing stored to send.
		if sub.def.initial != nil {
			msgs, err := sub.def.initial(ctx, s, sub.node)
			if err != nil {
				return err
			}
			for _, m := range msgs {
				if err := s.emit(ctx, m, s.snap); err != nil {
					return err
				}
			}
		}
		if err := s.emit(ctx, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL_DONE, Topic: sub.def.topic,
		}, s.snap); err != nil {
			return err
		}
	}
	return nil
}

// replay re-sends the logged changes after the resume token for the
// subscriptions the token covers.
func (s *watchSession) replay(ctx context.Context) error {
	if !s.hasResume {
		return nil
	}
	covered := map[hubv1.WatchTopic][]*filterNode{}
	for _, sub := range s.subs {
		if sub.covered {
			covered[sub.def.topic] = append(covered[sub.def.topic], sub.node)
		}
	}
	if len(covered) == 0 {
		return nil
	}
	entries, _, _ := s.bus.snapshot(s.resumeSeq, s.snap)
	for _, e := range entries {
		for _, node := range covered[e.change.topic] {
			m, ok := replayOne(e, node)
			if !ok {
				continue
			}
			if err := s.emit(ctx, m, e.seq); err != nil {
				return err
			}
		}
	}
	return nil
}

// replayOne applies one logged or live change to one subscription: the
// change, or UNMATCHED when the resource left the filter, or nothing.
func replayOne(e watchEntry, node *filterNode) (*hubv1.WatchResponse, bool) {
	matched := e.change.match == nil || e.change.match(node)
	switch e.change.change {
	case hubv1.ChangeType_CHANGE_TYPE_CREATED:
		if !matched {
			return nil, false
		}
		return e.change.msg, true
	case hubv1.ChangeType_CHANGE_TYPE_UPDATED:
		if matched {
			return e.change.msg, true
		}
		if e.change.was != nil && e.change.was(node) {
			out, _ := proto.Clone(e.change.msg).(*hubv1.WatchResponse)
			out.ChangeType = hubv1.ChangeType_CHANGE_TYPE_UNMATCHED
			return out, true
		}
		return nil, false
	case hubv1.ChangeType_CHANGE_TYPE_DELETED:
		if !matched {
			return nil, false
		}
		return e.change.msg, true
	default:
		// The bus only logs CREATED, UPDATED and DELETED; anything else
		// matches nothing.
		return nil, false
	}
}

// live pumps bus entries until the context ends, paced by the checkpoint
// and summary timers. A slow consumer keeps only the latest pane and
// summary values; falling behind on a never-dropped topic ends the stream
// with RESYNC_REQUIRED/FELL_BEHIND (02c).
func (s *watchSession) live(ctx context.Context) error {
	var pending []watchEntry
	panes := map[string]watchEntry{}
	var summary *watchEntry
	checkpoint := time.NewTimer(s.checkpoint)
	defer checkpoint.Stop()
	var summaryTimer *time.Timer
	var summaryCh <-chan time.Time
	defer func() {
		if summaryTimer != nil {
			summaryTimer.Stop()
		}
	}()
	resetCheckpoint := func() {
		if !checkpoint.Stop() {
			select {
			case <-checkpoint.C:
			default:
			}
		}
		checkpoint.Reset(s.checkpoint)
	}
	ingest := func(e watchEntry) {
		for _, sub := range s.subs {
			if sub.def.topic != e.change.topic {
				continue
			}
			m, ok := replayOne(e, sub.node)
			if !ok {
				continue
			}
			// Queue the converted message: a resource that left the
			// filter goes out once as UNMATCHED, not as the update.
			out := e
			out.change.msg = m
			if e.change.droppable() {
				if e.change.topic == hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES {
					panes[paneKeyOf(m)] = out
				} else if summary == nil || e.seq >= summary.seq {
					summary = &out
				}
			} else {
				pending = append(pending, out)
			}
		}
		stashPanes, stashSummary := s.sub.drainStash()
		for k, e := range stashPanes {
			if prev, dup := panes[k]; !dup || e.seq >= prev.seq {
				panes[k] = e
			}
		}
		if stashSummary != nil && (summary == nil || stashSummary.seq >= summary.seq) {
			summary = stashSummary
		}
	}
	// sendPending sends one round: queued changes first, then every
	// pending pane, then the summary when its second has passed.
	sendPending := func() (bool, error) {
		if len(pending) > 0 {
			e := pending[0]
			pending = pending[1:]
			if err := s.emit(ctx, e.change.msg, e.seq); err != nil {
				return false, err
			}
			resetCheckpoint()
			return true, nil
		}
		if len(panes) > 0 {
			keys := make([]string, 0, len(panes))
			for k := range panes {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				e := panes[k]
				delete(panes, k)
				if err := s.emit(ctx, e.change.msg, e.seq); err != nil {
					return false, err
				}
			}
			resetCheckpoint()
			return true, nil
		}
		if summary != nil {
			if wait := watchSummaryInterval - s.now().Sub(s.summaryAt); wait > 0 {
				if summaryTimer == nil {
					summaryTimer = time.NewTimer(wait)
				} else {
					summaryTimer.Reset(wait)
				}
				summaryCh = summaryTimer.C
				return false, nil
			}
			e := summary
			summary = nil
			s.summaryAt = s.now()
			if err := s.emit(ctx, e.change.msg, e.seq); err != nil {
				return false, err
			}
			resetCheckpoint()
			return true, nil
		}
		return false, nil
	}
	for {
		if s.sub.isBehind() {
			return s.resync(ctx, hubv1.ResyncReason_RESYNC_REASON_FELL_BEHIND)
		}
		select {
		case e := <-s.sub.ch:
			ingest(e)
			if s.sub.isBehind() {
				return s.resync(ctx, hubv1.ResyncReason_RESYNC_REASON_FELL_BEHIND)
			}
		case <-summaryCh:
			summaryCh = nil
		case <-checkpoint.C:
			if err := s.emit(ctx, &hubv1.WatchResponse{
				ChangeType: hubv1.ChangeType_CHANGE_TYPE_CHECKPOINT,
			}, s.lastSeq); err != nil {
				return err
			}
			resetCheckpoint()
		case <-ctx.Done():
			return nil
		}
		for {
			sent, err := sendPending()
			if err != nil {
				return err
			}
			if !sent {
				break
			}
			if s.sub.isBehind() {
				return s.resync(ctx, hubv1.ResyncReason_RESYNC_REASON_FELL_BEHIND)
			}
		}
	}
}

// resync ends the stream with RESYNC_REQUIRED and closes cleanly: the
// client reopens with send_initial and no token (02c). No resume token is
// minted, so a client cannot resume from a dead position.
func (s *watchSession) resync(ctx context.Context, reason hubv1.ResyncReason) error {
	s.resyncs.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason.String())))
	msg := &hubv1.WatchResponse{
		ChangeType: hubv1.ChangeType_CHANGE_TYPE_RESYNC_REQUIRED, ResyncReason: reason, HubSeq: s.lastSeq,
	}
	return sendErr(ctx, s.send(msg))
}
