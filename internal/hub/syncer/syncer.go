package syncer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Intervals of the poll loop (defaults.md, 03m). FullPollEvery bounds a
// false not_modified: the periodic full poll runs without validators.
const (
	WorkItemsEvery      = 60 * time.Second
	ChangeRequestsEvery = 60 * time.Second
	ClaimsEvery         = 5 * time.Minute
	FullPollEvery       = 30 * time.Minute
	// SlowFactor stretches poll intervals four times under 10 % of a
	// budget (03j).
	SlowFactor = 4
	// maxUnavailableBackoff caps the exponential back-off on outages (03m).
	maxUnavailableBackoff = 5 * time.Minute
	// skewWarn is the provider-clock skew that logs sync.provider_skew
	// (03j: provider timestamps stay untrusted ordering evidence past it).
	skewWarn = 5 * time.Minute
)

// Resources name the per-resource polling state and lag.
const (
	ResourceWorkItems      = "work_items"
	ResourceClaims         = "claims"
	ResourceChangeRequests = "change_requests"
)

// RepoGate is the trusted-issues configuration of one repo: its label and
// extra users, and whether its items may leave SEEN. Row 13a owns the
// spec; row 11 reads enabled and inherits the fleet gate.
type RepoGate struct {
	Label   string
	Users   []*hubv1.TrustedUser
	Enabled bool
}

// TriageNotice is one triage raise or retire for the loop's sources.
// The triage_items write lands with row 12; until then the daemon logs
// the notice (sync.access_lost, sync.double_claim) and counts it, with
// the 03d fingerprint for dedupe.
type TriageNotice struct {
	Kind        string
	Integration string
	Project     string
	Fingerprint string
	Detail      string
	Retire      bool
	Time        time.Time
}

// Syncer polls the provider adapters and keeps the mirrors (03m W4). One
// Syncer serves every integration; the daemon runs one Loop goroutine per
// integration with a context it cancels on shutdown.
type Syncer struct {
	Store    *store.Store
	Adapters map[string]Adapter
	// Gate is the fleet trusted-issues gate; nil trusts nothing.
	Gate *hubv1.TrustedGate
	// RepoGate resolves a project's gate override; nil inherits the
	// fleet gate on an enabled repo.
	RepoGate func(project string) RepoGate
	// FleetUsers are the fleet's own provider logins (ForgeCliStatus.user
	// of every machine) for by_fleet; empty matches nothing, so every
	// claim reads foreign until row 22 wires the machine users in.
	FleetUsers map[string]bool
	// OnTriage receives raises and retires; nil drops them.
	OnTriage func(TriageNotice)
	// OnWorkItems receives the work-item mirror changes a page or claim
	// write committed, in commit order, for Watch subscribers; nil drops
	// them. The daemon wires it to the Fleet.
	OnWorkItems func(context.Context, []statedb.WorkItemChange)
	// OnChangeRequests receives the change-request mirror changes a
	// page committed, as OnWorkItems.
	OnChangeRequests func(context.Context, []statedb.ChangeRequestChange)
	Logger           *slog.Logger
	Metrics          *Metrics
	// Tracer draws the hub.sync.* spans; nil uses the global provider.
	Tracer trace.Tracer
	// Clock and intervals are seams for the fake clock in tests.
	Clock               func() time.Time
	WorkItemsEvery      time.Duration
	ChangeRequestsEvery time.Duration
	ClaimsEvery         time.Duration
	FullPollEvery       time.Duration
	MaxSyncLag          time.Duration

	mu       sync.Mutex
	health   map[string]*integrationHealth
	branches map[string]map[string][]string
	noticed  map[string]bool
}

// publishWorkItems hands committed mirror changes to Watch subscribers;
// nil and empty publish nothing.
func (s *Syncer) publishWorkItems(ctx context.Context, changes []statedb.WorkItemChange) {
	if s.OnWorkItems != nil && len(changes) > 0 {
		s.OnWorkItems(ctx, changes)
	}
}

// publishChangeRequests hands committed change-request changes to Watch
// subscribers, as publishWorkItems.
func (s *Syncer) publishChangeRequests(ctx context.Context, changes []statedb.ChangeRequestChange) {
	if s.OnChangeRequests != nil && len(changes) > 0 {
		s.OnChangeRequests(ctx, changes)
	}
}

type integrationHealth struct {
	health          hubv1.IntegrationHealth
	reason          hubv1.IntegrationHealthReason
	message         string
	lastErr         *integrationv1.ProviderError
	limits          map[int32]*integrationv1.RateLimitState
	backoffUntil    time.Time
	unavailableBack time.Duration
	slow            bool
	consecutiveOK   int
	lastComplete    map[string]time.Time
	providerName    string
	capabilities    *integrationv1.IntegrationCapabilities
}

// New builds the loop with the default intervals (defaults.md); MaxSyncLag
// defaults to timing.max_sync_lag (10 min).
func New(st *store.Store, adapters map[string]Adapter, gate *hubv1.TrustedGate, logger *slog.Logger, metrics *Metrics) *Syncer {
	return &Syncer{
		Store: st, Adapters: adapters, Gate: gate,
		FleetUsers:          map[string]bool{},
		Logger:              logger,
		Metrics:             metrics,
		Clock:               time.Now,
		WorkItemsEvery:      WorkItemsEvery,
		ChangeRequestsEvery: ChangeRequestsEvery,
		ClaimsEvery:         ClaimsEvery,
		FullPollEvery:       FullPollEvery,
		MaxSyncLag:          10 * time.Minute,
		health:              map[string]*integrationHealth{},
		branches:            map[string]map[string][]string{},
		noticed:             map[string]bool{},
	}
}

func (s *Syncer) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Syncer) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

// tracer draws the 03m spans; without one the global provider stands in.
func (s *Syncer) tracer() trace.Tracer {
	if s.Tracer != nil {
		return s.Tracer
	}
	return otel.Tracer(metricsScope)
}

func (s *Syncer) adapter(id string) (Adapter, bool) {
	a, ok := s.Adapters[id]
	return a, ok
}

func (s *Syncer) stateFor(id string) *integrationHealth {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.health[id]
	if !ok {
		st = &integrationHealth{
			health:       hubv1.IntegrationHealth_INTEGRATION_HEALTH_OK,
			limits:       map[int32]*integrationv1.RateLimitState{},
			lastComplete: map[string]time.Time{},
		}
		if a, ok := s.Adapters[id]; ok && a != nil {
			caps := a.Describe()
			st.capabilities = caps
			if caps != nil {
				st.providerName = caps.GetProviderName()
			}
		}
		s.health[id] = st
	}
	return st
}

// PollOnce runs one complete round for the integration: work items per
// watched repo, claims per candidate item, and the registered branches.
// A full round runs without validators. Rounds continue past a per-repo
// failure and report every failure joined.
func (s *Syncer) PollOnce(ctx context.Context, id string, full bool) error {
	a, ok := s.adapter(id)
	if !ok || a == nil {
		return fmt.Errorf("sync: no adapter for %q", id)
	}
	now := s.now()
	st := s.stateFor(id)
	s.mu.Lock()
	backoff := st.backoffUntil
	s.mu.Unlock()
	if now.Before(backoff) {
		return nil
	}
	repos, err := statedb.ListWatchedRepos(ctx, s.Store, id)
	if err != nil {
		return err
	}
	var errs []error
	for _, repo := range repos {
		if err := s.workItemsRound(ctx, a, id, repo, full, now); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.claimsRound(ctx, a, id, now); err != nil {
		errs = append(errs, err)
	}
	if err := s.branchesRound(ctx, a, id, now); err != nil {
		errs = append(errs, err)
	}
	joined := errors.Join(errs...)
	s.finishRound(ctx, id, joined, now)
	return joined
}

// Loop polls until ctx ends, on the row intervals (stretched four times
// under 10 % of a budget). The first round runs at once; a full poll runs
// every 30 minutes.
func (s *Syncer) Loop(ctx context.Context, id string) {
	if err := s.PollOnce(ctx, id, false); err != nil && !errors.Is(err, context.Canceled) {
		s.log().WarnContext(ctx, "sync.round_failed", "integration", id, "error", err.Error())
	}
	itemsEvery, changesEvery, claimsEvery, fullEvery := s.intervals(id)
	items, changes, claims, full := time.NewTicker(itemsEvery), time.NewTicker(changesEvery),
		time.NewTicker(claimsEvery), time.NewTicker(fullEvery)
	defer items.Stop()
	defer changes.Stop()
	defer claims.Stop()
	defer full.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-items.C:
			_ = now
			if err := s.PollOnce(ctx, id, false); err != nil {
				s.log().WarnContext(ctx, "sync.round_failed", "integration", id, "error", err.Error())
			}
		case <-changes.C:
			if err := s.branchesRound(ctx, mustAdapter(s, id), id, s.now()); err != nil {
				s.log().WarnContext(ctx, "sync.round_failed", "integration", id, "error", err.Error())
			}
		case <-claims.C:
			if err := s.claimsRound(ctx, mustAdapter(s, id), id, s.now()); err != nil {
				s.log().WarnContext(ctx, "sync.round_failed", "integration", id, "error", err.Error())
			}
		case <-full.C:
			if err := s.PollOnce(ctx, id, true); err != nil {
				s.log().WarnContext(ctx, "sync.round_failed", "integration", id, "error", err.Error())
			}
		}
	}
}

func mustAdapter(s *Syncer, id string) Adapter {
	a, _ := s.adapter(id)
	return a
}

func (s *Syncer) intervals(id string) (items, changes, claims, full time.Duration) {
	items, changes, claims, full = s.WorkItemsEvery, s.ChangeRequestsEvery, s.ClaimsEvery, s.FullPollEvery
	if items <= 0 {
		items = WorkItemsEvery
	}
	if changes <= 0 {
		changes = ChangeRequestsEvery
	}
	if claims <= 0 {
		claims = ClaimsEvery
	}
	if full <= 0 {
		full = FullPollEvery
	}
	st := s.stateFor(id)
	s.mu.Lock()
	slow := st.slow
	s.mu.Unlock()
	if slow {
		items *= SlowFactor
		changes *= SlowFactor
		claims *= SlowFactor
	}
	return items, changes, claims, full
}

// finishRound closes the round: health returns to OK only after one
// complete round without error (03j); access-lost notices retire then.
func (s *Syncer) finishRound(ctx context.Context, id string, roundErr error, now time.Time) {
	st := s.stateFor(id)
	s.mu.Lock()
	if roundErr == nil {
		st.consecutiveOK++
		st.health = hubv1.IntegrationHealth_INTEGRATION_HEALTH_OK
		st.reason = hubv1.IntegrationHealthReason_INTEGRATION_HEALTH_REASON_UNSPECIFIED
		st.message = ""
		st.lastErr = nil
		st.unavailableBack = 0
	} else {
		st.consecutiveOK = 0
	}
	s.mu.Unlock()
	s.persistStatus(ctx, id, now)
	if roundErr == nil {
		s.retireNotices(id, now)
	}
	if s.Metrics != nil {
		s.Metrics.SetState(id, st.health.String())
		for resource, t := range st.lastComplete {
			s.Metrics.SetLag(id, resource, now.Sub(t).Seconds())
		}
	}
}

// fail records a provider failure: health, back-off and the triage notice
// (03j table), then persists the status and returns the error.
func (s *Syncer) fail(ctx context.Context, id, project, resource string, err error, now time.Time) error {
	provider := providerErrorOf(err)
	st := s.stateFor(id)
	s.mu.Lock()
	st.lastErr = provider
	st.consecutiveOK = 0
	switch provider.GetKind() {
	case integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_NOT_FOUND,
		integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_FORBIDDEN:
		st.health = hubv1.IntegrationHealth_INTEGRATION_HEALTH_DEGRADED
		st.reason = hubv1.IntegrationHealthReason_INTEGRATION_HEALTH_REASON_ACCESS_LOST
		st.message = "access lost for " + project
		s.raiseLocked(id, project, "integration_access_lost", st.message, now)
	case integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAUTHENTICATED:
		st.health = hubv1.IntegrationHealth_INTEGRATION_HEALTH_DEGRADED
		st.reason = hubv1.IntegrationHealthReason_INTEGRATION_HEALTH_REASON_CREDENTIALS_INVALID
		st.message = "credential invalid"
		s.raiseLocked(id, project, "integration_access_lost", st.message, now)
	case integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_RATE_LIMITED:
		st.health = hubv1.IntegrationHealth_INTEGRATION_HEALTH_BACKING_OFF
		st.reason = hubv1.IntegrationHealthReason_INTEGRATION_HEALTH_REASON_RATE_LIMITED
		wait := provider.GetRetryAfter().AsDuration()
		if rl := provider.GetRateLimit(); rl != nil && rl.GetResetTime() != nil {
			if until := rl.GetResetTime().AsTime().Sub(now); until > wait {
				wait = until
			}
		}
		if wait < 0 {
			wait = 0
		}
		st.backoffUntil = now.Add(wait)
		st.message = "rate limited"
		s.log().InfoContext(ctx, "sync.rate_limited", "integration", id,
			"resource", resource, "wait", wait.String())
		if s.Metrics != nil {
			kind := "CORE"
			if provider.GetRateLimit() != nil {
				kind = provider.GetRateLimit().GetKind().String()
			}
			s.Metrics.rateLimits.Add(ctx, 1,
				metricWith("integration", id, "kind", kind))
		}
	case integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAVAILABLE:
		st.health = hubv1.IntegrationHealth_INTEGRATION_HEALTH_BACKING_OFF
		st.reason = hubv1.IntegrationHealthReason_INTEGRATION_HEALTH_REASON_PROVIDER_UNAVAILABLE
		back := st.unavailableBack
		if back <= 0 {
			back = 5 * time.Second
		} else {
			back *= 2
		}
		if back > maxUnavailableBackoff {
			back = maxUnavailableBackoff
		}
		st.unavailableBack = back
		st.backoffUntil = now.Add(back)
		st.message = "provider unavailable"
	case integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_CURSOR_EXPIRED:
		s.log().InfoContext(ctx, "sync.cursor_reset", "integration", id, "resource", resource)
	default:
		s.log().WarnContext(ctx, "sync.provider_error", "integration", id,
			"resource", resource, "kind", provider.GetKind().String(), "message", provider.GetMessage())
	}
	kind := provider.GetKind()
	s.mu.Unlock()
	s.persistStatus(ctx, id, now)
	if s.Metrics != nil {
		s.Metrics.requests.Add(ctx, 1, metricWith("integration", id, "resource", resource, "status", "error"))
		_ = kind
	}
	return err
}

func metricWith(kv ...string) metric.MeasurementOption {
	attrs := make([]attribute.KeyValue, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		attrs = append(attrs, attribute.String(kv[i], kv[i+1]))
	}
	return metric.WithAttributes(attrs...)
}
