package syncer

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// providerErrorOf extracts the adapter's ProviderError; a Go-level failure
// (transport down, a bug above the adapter) reads as UNAVAILABLE with
// back-off, never as success.
func providerErrorOf(err error) *integrationv1.ProviderError {
	var call *CallError
	if errors.As(err, &call) && call.Provider != nil {
		return call.Provider
	}
	return &integrationv1.ProviderError{
		Kind:      integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAVAILABLE,
		Retryable: true,
		Message:   err.Error(),
	}
}

// mergeLimits remembers the latest budget per kind for the status row.
func (s *Syncer) mergeLimits(id string, meta *integrationv1.ResponseMeta) {
	if meta == nil {
		return
	}
	st := s.stateFor(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	slow := false
	for _, l := range meta.GetRateLimits() {
		st.limits[int32(l.GetKind())] = l
		if l.GetLimit() > 0 && uint64(l.GetRemaining())*10 < uint64(l.GetLimit()) {
			slow = true
		}
	}
	st.slow = slow
}

// rateLimitValues carries the response budgets into the page transaction.
func rateLimitValues(meta *integrationv1.ResponseMeta) []statedb.RateLimitValue {
	if meta == nil {
		return nil
	}
	var out []statedb.RateLimitValue
	for _, l := range meta.GetRateLimits() {
		v := statedb.RateLimitValue{Kind: int32(l.GetKind()), Limit: l.GetLimit(), Remaining: l.GetRemaining()}
		if ts := l.GetResetTime(); ts != nil {
			v.ResetMs, v.HasReset = ts.AsTime().UnixMilli(), true
		}
		out = append(out, v)
	}
	return out
}

// checkSkew logs provider timestamps drifting past 5 minutes (03j: past
// it they stay untrusted ordering evidence).
func (s *Syncer) checkSkew(ctx context.Context, id string, meta *integrationv1.ResponseMeta, now time.Time) {
	if meta == nil || meta.GetProviderTime() == nil {
		return
	}
	if skew := now.Sub(meta.GetProviderTime().AsTime()); skew < 0 {
		skew = -skew
		if skew > skewWarn {
			s.log().WarnContext(ctx, "sync.provider_skew", "integration", id, "skew", skew.String())
		}
	} else if skew > skewWarn {
		s.log().WarnContext(ctx, "sync.provider_skew", "integration", id, "skew", skew.String())
	}
}

// storeLimits persists the budgets of a 304 round with the same cursor.
func (s *Syncer) storeLimits(ctx context.Context, id, resource, page string, cursor statedb.StoredCursor, meta *integrationv1.ResponseMeta, now time.Time) {
	out := statedb.CursorValue{Resource: resource, Page: page, ETag: cursor.ETag, Opaque: cursor.Opaque}
	if cursor.HasLastMod {
		out.LastModifiedMs, out.HasLastMod = cursor.LastModifiedMs, true
	}
	if cursor.HasSince {
		out.SinceMs, out.HasSince = cursor.SinceMs, true
	}
	_, _ = statedb.UpsertWorkItemPage(ctx, s.Store, id, nil,
		[]statedb.CursorValue{out}, rateLimitValues(meta), now.UnixMilli())
}

// ok records a clean round: metrics, lag and the completion stamp the
// planner's max_sync_lag reads through the status row.
func (s *Syncer) ok(ctx context.Context, id, resource, status string, start, now time.Time) {
	st := s.stateFor(id)
	s.mu.Lock()
	st.lastComplete[resource] = now
	s.mu.Unlock()
	if s.Metrics != nil {
		s.Metrics.requests.Add(ctx, 1, metricWith("integration", id, "resource", resource, "status", status))
		s.Metrics.poll.Record(ctx, now.Sub(start).Seconds(), metricWith("integration", id, "resource", resource))
		s.Metrics.SetLag(id, resource, 0)
	}
}

// raise emits a triage notice once while its condition holds.
func (s *Syncer) raise(id, project, kind, detail string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raiseLocked(id, project, kind, detail, now)
}

func (s *Syncer) raiseLocked(id, project, kind, detail string, now time.Time) {
	fingerprint := kind + ":" + id + ":" + project
	if s.noticed[fingerprint] {
		return
	}
	s.noticed[fingerprint] = true
	if s.OnTriage != nil {
		s.OnTriage(TriageNotice{
			Kind: kind, Integration: id, Project: project,
			Fingerprint: fingerprint, Detail: detail, Time: now,
		})
	}
}

// retireNotices retires access-lost notices once the round is clean again.
func (s *Syncer) retireNotices(id string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for fingerprint := range s.noticed {
		var kind, rest string
		if i := indexByte(fingerprint, ':'); i >= 0 {
			kind, rest = fingerprint[:i], fingerprint[i+1:]
		}
		if kind != "integration_access_lost" || !hasPrefix(rest, id+":") {
			continue
		}
		delete(s.noticed, fingerprint)
		if s.OnTriage != nil {
			s.OnTriage(TriageNotice{
				Kind: kind, Integration: id, Project: trimPrefix(rest, id+":"),
				Fingerprint: fingerprint, Retire: true, Time: now,
			})
		}
	}
}

func indexByte(str string, b byte) int {
	for i := 0; i < len(str); i++ {
		if str[i] == b {
			return i
		}
	}
	return -1
}

func hasPrefix(str, prefix string) bool {
	return len(str) >= len(prefix) && str[:len(prefix)] == prefix
}

func trimPrefix(str, prefix string) string {
	if hasPrefix(str, prefix) {
		return str[len(prefix):]
	}
	return str
}

// persistStatus rewrites the integration's status row (health, back-off,
// lag, budgets) for the UI and the planner; a failure is logged, since
// the round's own writes already reported the store error.
func (s *Syncer) persistStatus(ctx context.Context, id string, now time.Time) {
	st := s.stateFor(id)
	s.mu.Lock()
	status := &hubv1.IntegrationStatus{
		ObservedTime:  timestamppb.New(now),
		Provider:      st.capabilities.GetProvider(),
		ProviderName:  st.providerName,
		Capabilities:  st.capabilities,
		Health:        st.health,
		HealthReason:  st.reason,
		HealthMessage: st.message,
		LastError:     st.lastErr,
		LastSyncTime:  timestamppb.New(now),
	}
	for _, l := range st.limits {
		status.RateLimits = append(status.RateLimits, l)
	}
	if !st.backoffUntil.IsZero() {
		status.BackoffUntilTime = timestamppb.New(st.backoffUntil)
	}
	var lag time.Duration
	for _, t := range st.lastComplete {
		if d := now.Sub(t); d > lag {
			lag = d
		}
	}
	status.SyncLag = durationpb.New(lag)
	s.mu.Unlock()
	body, err := protojson.Marshal(status)
	if err != nil {
		s.log().WarnContext(ctx, "sync.status_failed", "integration", id, "error", err.Error())
		return
	}
	if err := statedb.SetIntegrationStatus(ctx, s.Store, id, body, now.UnixMilli()); err != nil {
		s.log().WarnContext(ctx, "sync.status_failed", "integration", id, "error", err.Error())
	}
}
