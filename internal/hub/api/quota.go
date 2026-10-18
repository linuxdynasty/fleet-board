package api

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/types/known/durationpb"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// quotaClass is closed; callers choose the class at the validated execution
// point, after higher-priority errors and before a state-changing effect.
type quotaClass string

const (
	quotaRead    quotaClass = "read"
	quotaWrite   quotaClass = "write"
	quotaPublic  quotaClass = "unauthenticated"
	quotaMetrics quotaClass = "metrics"
)

const quotaWindow = time.Minute

// maxPeerQuotaStates is a memory bound, not a request budget. Each peer has
// two small fixed-window counters and a canonical IP key. Even with map and
// string overhead, 1024 records are well below a MiB. A full table never
// evicts an indebted peer or resets its allowance on a new connection.
const maxPeerQuotaStates = 1024

type quotaWindowState struct {
	started time.Time
	used    int
}

func (w *quotaWindowState) take(now time.Time, limit int) (time.Duration, bool) {
	if w.used == 0 || !now.Before(w.started.Add(quotaWindow)) {
		w.started, w.used = now, 0
	}
	if w.used >= limit {
		return w.started.Add(quotaWindow).Sub(now), false
	}
	w.used++
	return 0, true
}

func (w quotaWindowState) debtUntil(now time.Time) time.Time {
	if w.used == 0 || !now.Before(w.started.Add(quotaWindow)) {
		return time.Time{}
	}
	return w.started.Add(quotaWindow)
}

type peerQuota struct {
	public, metrics quotaWindowState
}

func (p peerQuota) debtUntil(now time.Time) time.Time {
	a, b := p.public.debtUntil(now), p.metrics.debtUntil(now)
	if b.After(a) {
		return b
	}
	return a
}

// Quotas is shared by the API and optional metrics listeners for one hub run.
// Its mutex covers only counters and map entries. A writer may call it inside
// a store transaction after the transaction's checks; the quota mutex is
// released before any state change or network call.
type Quotas struct {
	mu     sync.Mutex
	peers  map[netip.Addr]*peerQuota
	read   quotaWindowState
	write  quotaWindowState
	limits config.RateLimits
	now    func() time.Time
	denied metric.Int64Counter
}

// NewQuotas takes the checked fleetd.yaml limits. now is nil in production.
func NewQuotas(limits config.RateLimits, tel *telemetry.Telemetry, now func() time.Time) (*Quotas, error) {
	if tel == nil || limits.Read <= 0 || limits.Write <= 0 || limits.Unauthenticated <= 0 || limits.Metrics <= 0 {
		return nil, errors.New("api: quotas need telemetry and positive configured limits")
	}
	if now == nil {
		now = time.Now
	}
	denied, err := tel.Meter(scope).Int64Counter(tel.MetricPrefix()+"_api_rate_limited_total",
		metric.WithDescription("API requests refused by the configured rate or bounded peer table, by class and cause."))
	if err != nil {
		return nil, err
	}
	return &Quotas{peers: make(map[netip.Addr]*peerQuota), limits: limits, now: now, denied: denied}, nil
}

type quotaContextKey struct{}

type quotaRequest struct {
	mu     sync.Mutex
	quota  *Quotas
	peer   netip.Addr
	class  quotaClass
	result error
	done   bool
}

func withQuota(ctx context.Context, q *Quotas, peer netip.Addr) context.Context {
	return context.WithValue(ctx, quotaContextKey{}, &quotaRequest{quota: q, peer: peer})
}

// quotaCheckpoint reserves one request allowance, at most once. A handler
// calls it after its own identity, scope, resource, field, idempotency and
// mutable-state checks but before the first durable or external effect.
// Admin-socket requests have no TCP quota context and are unchanged.
func quotaCheckpoint(ctx context.Context, class quotaClass) error {
	qr, _ := ctx.Value(quotaContextKey{}).(*quotaRequest)
	if qr == nil {
		return nil
	}
	qr.mu.Lock()
	defer qr.mu.Unlock()
	if qr.done {
		if qr.class != class {
			return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
				"a request used two quota classes", nil, nil)
		}
		return qr.result
	}
	qr.class, qr.done = class, true
	qr.result = qr.quota.take(ctx, qr.peer, class)
	return qr.result
}

func (q *Quotas) take(ctx context.Context, peer netip.Addr, class quotaClass) error {
	q.mu.Lock()
	now := q.now()
	var window *quotaWindowState
	var limit int
	var wait time.Duration
	var capacity bool
	switch class {
	case quotaRead:
		window, limit = &q.read, q.limits.Read
	case quotaWrite:
		window, limit = &q.write, q.limits.Write
	case quotaPublic, quotaMetrics:
		entry := q.peers[peer]
		if entry == nil {
			if len(q.peers) >= maxPeerQuotaStates {
				entry, wait = q.reclaimOrWait(now)
				if entry == nil {
					capacity = true
				}
			}
			if !capacity {
				entry = &peerQuota{}
				q.peers[peer] = entry
			}
		}
		if !capacity {
			if class == quotaPublic {
				window, limit = &entry.public, q.limits.Unauthenticated
			} else {
				window, limit = &entry.metrics, q.limits.Metrics
			}
		}
	default:
		q.mu.Unlock()
		return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"unknown quota class", nil, nil)
	}
	allowed := false
	if !capacity {
		wait, allowed = window.take(now, limit)
	}
	q.mu.Unlock()
	if allowed {
		return nil
	}
	cause := "budget"
	if capacity {
		cause = "capacity"
	}
	q.denied.Add(ctx, 1, metric.WithAttributes(attribute.String("class", string(class)), attribute.String("cause", cause)))
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	e := connect.NewError(connect.CodeResourceExhausted, errors.New("request rate limited"))
	if d, err := connect.NewErrorDetail(&typev1.ErrorDetail{
		Reason: typev1.ReasonCode_REASON_CODE_RATE_LIMITED, Retryable: true,
		RetryAfter: durationpb.New(wait),
	}); err == nil {
		e.AddDetail(d)
	}
	return e
}

// reclaimOrWait removes one fully expired peer. If every peer still has
// allowance debt, it returns the earliest safe time for a new peer to retry.
// The caller holds q.mu; the scan is bounded by maxPeerQuotaStates.
func (q *Quotas) reclaimOrWait(now time.Time) (*peerQuota, time.Duration) {
	var earliest time.Time
	for ip, entry := range q.peers {
		until := entry.debtUntil(now)
		if until.IsZero() {
			delete(q.peers, ip)
			return entry, 0
		}
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	return nil, earliest.Sub(now)
}
