package api

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// This file opens Watch streams (02c, 02k row 12): the caller check per
// topic, the subscription validation, the quota admission, and the resume
// position. The stream itself pumps in watch_session.go, the topics read
// in watch_topics.go and watch_initial.go, and the writes publish in
// watch_publish.go.

// Watch opens a server stream of resource changes (02c, 02k row 12): one
// filter per topic, resumable across reconnects. Order of checks, as 02b
// ranks them: the caller (per topic, when the stream opens), the fields,
// then the quota admission before the first read.
func (f *Fleet) Watch(ctx context.Context, req *connect.Request[hubv1.WatchRequest], stream *connect.ServerStream[hubv1.WatchResponse],
) error {
	msg := req.Msg
	// The caller comes before the fields (02b): without an identity the
	// stream is unauthenticated, whatever it subscribes to.
	if PrincipalFrom(ctx).GetId().GetValue() == "" {
		return f.watchRefused(ctx, apiError(connect.CodeUnauthenticated,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID, "no caller identity", nil, nil))
	}
	explicit := len(msg.GetSubscriptions()) > 0
	subs := msg.GetSubscriptions()
	if !explicit {
		subs = defaultWatchSubs()
	}
	// The token parses before the scope check: a resume whose caller
	// lost a covered topic's scope resyncs as SCOPES_CHANGED instead of
	// refusing (02c).
	var token watchToken
	hasToken := msg.GetResumeToken() != ""
	if hasToken {
		var err error
		token, err = parseWatchToken(msg.GetResumeToken())
		if err != nil {
			return f.watchRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_RESUME_TOKEN_INVALID, "resume_token does not parse", nil,
				[]*typev1.FieldViolation{{Field: "resume_token", Description: "resume_token does not parse"}}))
		}
	}
	// firstSkipped names the scope the default set dropped first, for the
	// refusal when nothing remains.
	firstSkipped := ""
	// scopesChanged marks a resume that lost a covered topic's scope.
	scopesChanged := false
	seen := map[hubv1.WatchTopic]int{}
	parsed := make([]*watchSubscription, 0, len(subs))
	for i, sub := range subs {
		def := watchTopics[sub.GetTopic()]
		if def == nil {
			return f.watchRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"topic "+sub.GetTopic().String()+" is not served yet", nil,
				[]*typev1.FieldViolation{{
					Field:       "subscriptions[" + itoa(i) + "].topic",
					Description: "topic " + sub.GetTopic().String() + " is not served yet",
				}}))
		}
		if prev, dup := seen[sub.GetTopic()]; dup {
			return f.watchRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"topic "+sub.GetTopic().String()+" is subscribed twice", nil,
				[]*typev1.FieldViolation{{
					Field:       "subscriptions[" + itoa(i) + "].topic",
					Description: "topic " + sub.GetTopic().String() + " is already subscribed at index " + itoa(prev),
				}}))
		}
		seen[sub.GetTopic()] = i
		if _, err := authorizeScope(ctx, def.scope); err != nil {
			if hasToken && token.hasTopic(def.topic) {
				scopesChanged = true
				continue
			}
			if !explicit {
				if firstSkipped == "" {
					firstSkipped = def.scope
				}
				continue
			}
			return f.watchRefused(ctx, err)
		}
		parsed = append(parsed, &watchSubscription{
			def: def, filter: sub.GetFilter(), hash: filterHash(sub.GetFilter()),
			sendInitial: sub.GetSendInitial() || !explicit,
		})
	}
	for _, sub := range parsed {
		node, ferr := parseWatchFilter(sub.filter, sub.def)
		if ferr != nil {
			return f.watchRefused(ctx, apiError(connect.CodeInvalidArgument,
				typev1.ReasonCode_REASON_CODE_FILTER_INVALID, ferr.msg,
				map[string]string{"offset": itoa(ferr.offset)},
				[]*typev1.FieldViolation{{Field: "filter", Description: ferr.msg}}))
		}
		sub.node = node
	}
	// A lost scope on a covered topic resyncs the resume, so the client
	// reopens with send_initial over what is still allowed (02c). An
	// empty default set means the caller may read no Watch topic: the
	// stream opens refused, never silently empty.
	if scopesChanged {
		sess := &watchSession{
			bus: f.watch, instanceID: f.instanceID, resyncs: f.watchResyncs,
			send: stream.Send, logger: f.tel.Logger,
		}
		f.countWatch(ctx, "resync")
		return sess.resync(ctx, hubv1.ResyncReason_RESYNC_REASON_SCOPES_CHANGED)
	}
	if len(parsed) == 0 {
		return f.watchRefused(ctx, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_SCOPE_MISSING, "no Watch topic this caller may read",
			map[string]string{"scope": firstSkipped}, nil))
	}
	if hasToken {
		if token.IID != f.instanceID {
			sess := &watchSession{
				bus: f.watch, instanceID: f.instanceID, resyncs: f.watchResyncs,
				send: stream.Send, logger: f.tel.Logger,
			}
			f.countWatch(ctx, "resync")
			return sess.resync(ctx, hubv1.ResyncReason_RESYNC_REASON_HUB_RESTARTED)
		}
		for _, sub := range parsed {
			sub.covered = token.covers(sub.def.topic, sub.hash)
		}
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return err
	}
	var topics []hubv1.WatchTopic
	for _, sub := range parsed {
		topics = append(topics, sub.def.topic)
	}
	busSub, snap := f.watch.subscribe(topics)
	defer f.watch.unsubscribe(busSub)
	sess := &watchSession{
		bus: f.watch, instanceID: f.instanceID, store: f.store, subs: parsed,
		sub: busSub, snap: snap, send: stream.Send, now: f.now, checkpoint: f.checkpoint,
		summarize: func(ctx context.Context) (*hubv1.FleetSummary, error) {
			summary, _, err := f.buildFleetSummary(ctx)
			return summary, err
		},
		resyncs: f.watchResyncs, logger: f.tel.Logger,
	}
	// An idle stream emits nothing, so the checkpoint would mint a
	// sequence-zero token without this: start the emitted position at
	// the snapshot (or the token past it, pathologically).
	sess.lastSeq = snap
	if hasToken {
		sess.resumeSeq, sess.hasResume = token.Seq, true
		if token.Seq > sess.lastSeq {
			sess.lastSeq = token.Seq
		}
		if token.Seq < snap {
			// Only eviction proves a range is gone: hub_seq gaps
			// are normal (02c), so a token below the oldest
			// retained entry still replays when nothing evicted.
			if token.Seq < f.watch.evictedSeq() {
				f.countWatch(ctx, "resync")
				return sess.resync(ctx, hubv1.ResyncReason_RESYNC_REASON_TOKEN_TOO_OLD)
			}
		}
	}
	f.countWatch(ctx, "opened")
	f.tel.Logger.InfoContext(ctx, "watch.opened", "actor", watchActor(ctx), "topics", len(parsed))
	if err := sess.run(ctx); err != nil {
		// A client that went away ends its stream cancelled, not failed.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		return f.watchFailed(ctx, err)
	}
	return nil
}

// parseWatchFilter parses one subscription filter against its topic's
// catalogue. Topics without a filterable catalogue take only the empty
// filter (02c: FLEET_SUMMARY has no filter; approvals and operations lists
// take none yet).
func parseWatchFilter(filter string, def *watchTopicDef) (*filterNode, *filterError) {
	if !def.filtered && filter != "" {
		return nil, &filterError{msg: "topic " + def.topic.String() + " takes no filter"}
	}
	return parseFilter(filter, def.fields)
}

// defaultWatchSubs is the empty-request subscription: every served topic
// except pane summaries, unfiltered (02c). The open loop keeps only the
// topics the caller's scopes allow.
func defaultWatchSubs() []*hubv1.TopicSubscription {
	var out []*hubv1.TopicSubscription
	for _, topic := range watchTopicOrder {
		if topic == hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES {
			continue
		}
		out = append(out, &hubv1.TopicSubscription{Topic: topic})
	}
	return out
}

// watchActor names the caller for watch log lines: the id only, never a
// secret or a filter.
func watchActor(ctx context.Context) string {
	if p := PrincipalFrom(ctx); p != nil {
		return p.GetId().GetValue()
	}
	return ""
}

// countWatch counts a Watch open by its result: opened, refused or resync.
func (f *Fleet) countWatch(ctx context.Context, result string) {
	f.watchStreams.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// watchRefused counts and logs a Watch caller error by its reason.
func (f *Fleet) watchRefused(ctx context.Context, err error) error {
	f.countWatch(ctx, "refused")
	f.tel.Logger.InfoContext(ctx, "watch.refused", "reason", reasonOf(err), "actor", watchActor(ctx))
	return err
}

// watchFailed logs the hub's own Watch failure and returns INTERNAL
// without the cause's text.
func (f *Fleet) watchFailed(ctx context.Context, cause error) error {
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "watch.failed", "err_kind", kind, "actor", watchActor(ctx))
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the hub could not serve its watch", nil, nil)
}

func itoa(i int) string {
	return fmt.Sprint(i)
}
