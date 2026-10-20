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
	// firstSkipped names the scope the default set dropped first, for the
	// refusal when nothing remains.
	firstSkipped := ""
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
	// An empty default set means the caller may read no Watch topic: the
	// stream opens refused, never silently empty.
	if len(parsed) == 0 {
		return f.watchRefused(ctx, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_SCOPE_MISSING, "no Watch topic this caller may read",
			map[string]string{"scope": firstSkipped}, nil))
	}
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
		if token.IID != f.instanceID {
			sess := &watchSession{bus: f.watch, instanceID: f.instanceID, resyncs: f.watchResyncs, send: stream.Send}
			f.countWatch(ctx, "resync")
			f.tel.Logger.InfoContext(ctx, "watch.resync", "reason", "HUB_RESTARTED", "actor", watchActor(ctx))
			return sess.resync(ctx, hubv1.ResyncReason_RESYNC_REASON_HUB_RESTARTED)
		}
		for _, sub := range parsed {
			sub.covered = token.covers(sub.def.topic, sub.hash)
		}
	}
	if err := quotaCheckpoint(ctx, quotaRead); err != nil {
		return err
	}
	busSub, snap := f.watch.subscribe()
	defer f.watch.unsubscribe(busSub)
	sess := &watchSession{
		bus: f.watch, instanceID: f.instanceID, store: f.store, subs: parsed,
		sub: busSub, snap: snap, send: stream.Send, now: f.now, checkpoint: watchCheckpointInterval,
		summarize: func(ctx context.Context) (*hubv1.FleetSummary, error) {
			summary, _, err := f.buildFleetSummary(ctx)
			return summary, err
		},
		resyncs: f.watchResyncs,
	}
	if hasToken {
		sess.resumeSeq, sess.hasResume = token.Seq, true
		if token.Seq < snap {
			_, oldest, _ := f.watch.snapshot(token.Seq, snap)
			if oldest == 0 || token.Seq < oldest {
				f.countWatch(ctx, "resync")
				f.tel.Logger.InfoContext(ctx, "watch.resync", "reason", "TOKEN_TOO_OLD", "actor", watchActor(ctx))
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
