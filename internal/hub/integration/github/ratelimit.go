package github

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

// directive is what the hub does with a reported budget (03j): poll at the
// normal interval, slow four times under 10 % remaining, or stop before
// reset_time at zero.
type directive int

const (
	directiveGo directive = iota
	directiveSlow
	directiveStop
)

// pollDirective maps one budget to its directive. A budget the provider did
// not report (limit 0) polls normally: providers without budgets (Jira,
// Linear) filter by `since` only.
func pollDirective(remaining, limit uint32) directive {
	if limit == 0 {
		return directiveGo
	}
	if remaining == 0 {
		return directiveStop
	}
	if uint64(remaining)*10 < uint64(limit) {
		return directiveSlow
	}
	return directiveGo
}

// parseRateLimits reads one REST budget from x-ratelimit-* into a
// RateLimitState and the provider's clock from Date (sync.proto: the hub
// watches the skew past 5 minutes). No budget headers means none reported,
// not zero: the second return is empty and the time is zero.
func parseRateLimits(h http.Header, kind integrationv1.RateLimitKind) ([]*integrationv1.RateLimitState, time.Time) {
	limitRaw := strings.TrimSpace(h.Get("X-RateLimit-Limit"))
	if limitRaw == "" {
		return nil, parseProviderTime(h)
	}
	limit, err := strconv.ParseUint(limitRaw, 10, 32)
	if err != nil {
		return nil, parseProviderTime(h)
	}
	remaining, _ := strconv.ParseUint(strings.TrimSpace(h.Get("X-RateLimit-Remaining")), 10, 32)
	state := &integrationv1.RateLimitState{
		Kind:      kind,
		Limit:     uint32(limit),
		Remaining: uint32(remaining),
	}
	if resetRaw := strings.TrimSpace(h.Get("X-RateLimit-Reset")); resetRaw != "" {
		if unix, err := strconv.ParseInt(resetRaw, 10, 64); err == nil && unix > 0 {
			state.ResetTime = timestamppb.New(time.Unix(unix, 0).UTC())
		}
	}
	return []*integrationv1.RateLimitState{state}, parseProviderTime(h)
}

// parseProviderTime reads the Date header; garbage means zero, which the
// hub treats as "not reported".
func parseProviderTime(h http.Header) time.Time {
	raw := strings.TrimSpace(h.Get("Date"))
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC1123, time.RFC1123Z} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// graphQLRateLimit maps a GraphQL rateLimit object (limit, cost, remaining,
// resetAt RFC 3339) to the GRAPHQL budget. resetAt garbage leaves the reset
// unset rather than refusing the whole budget.
func graphQLRateLimit(limit, _ int, remaining int, resetAt string) *integrationv1.RateLimitState {
	state := &integrationv1.RateLimitState{
		Kind:      integrationv1.RateLimitKind_RATE_LIMIT_KIND_GRAPHQL,
		Limit:     uint32Saturating(limit),
		Remaining: uint32Saturating(remaining),
	}
	if resetAt != "" {
		if t, err := time.Parse(time.RFC3339, resetAt); err == nil {
			state.ResetTime = timestamppb.New(t.UTC())
		}
	}
	return state
}

// uint32Saturating clamps a provider JSON number into a uint32 budget:
// negatives read as zero, huge values as the maximum.
func uint32Saturating(n int) uint32 {
	if n <= 0 {
		return 0
	}
	if int64(n) > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n)
}
