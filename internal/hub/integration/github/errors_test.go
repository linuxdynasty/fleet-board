package github

import (
	"net/http"
	"testing"
	"time"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

func headers(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}

func TestClassifyStatus(t *testing.T) {
	t.Parallel()
	resetUnix := "1790859600"

	cases := []struct {
		name        string
		status      int
		header      http.Header
		body        string
		wantKind    integrationv1.ProviderErrorKind
		wantRetry   bool
		wantProject string
	}{
		{
			name:        "404 is not found",
			status:      404,
			header:      headers(),
			wantKind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_NOT_FOUND,
			wantRetry:   false,
			wantProject: "acme/web",
		},
		{
			name:      "401 is unauthenticated",
			status:    401,
			header:    headers(),
			wantKind:  integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAUTHENTICATED,
			wantRetry: false,
		},
		{
			name:      "403 without rate headers is forbidden",
			status:    403,
			header:    headers(),
			body:      `{"message": "Resource not accessible by integration"}`,
			wantKind:  integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_FORBIDDEN,
			wantRetry: false,
		},
		{
			name:   "403 with exhausted budget is rate limited",
			status: 403,
			header: headers(
				"X-RateLimit-Limit", "5000",
				"X-RateLimit-Remaining", "0",
				"X-RateLimit-Reset", resetUnix,
			),
			body:      `{"message": "API rate limit exceeded for installation"}`,
			wantKind:  integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_RATE_LIMITED,
			wantRetry: true,
		},
		{
			name:      "429 with retry-after is rate limited",
			status:    429,
			header:    headers("Retry-After", "120"),
			wantKind:  integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_RATE_LIMITED,
			wantRetry: true,
		},
		{
			name:      "500 is unavailable",
			status:    500,
			header:    headers(),
			wantKind:  integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAVAILABLE,
			wantRetry: true,
		},
		{
			name:      "422 is unsupported",
			status:    422,
			header:    headers(),
			wantKind:  integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			wantRetry: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyStatus(tc.status, tc.header, "acme/web", "GET https://api.github.com/x", "msg", tc.body)
			if got.GetKind() != tc.wantKind {
				t.Errorf("kind = %v, want %v", got.GetKind(), tc.wantKind)
			}
			if got.GetRetryable() != tc.wantRetry {
				t.Errorf("retryable = %v, want %v", got.GetRetryable(), tc.wantRetry)
			}
			if tc.wantProject != "" && got.GetProject() != tc.wantProject {
				t.Errorf("project = %q, want %q", got.GetProject(), tc.wantProject)
			}
			if got.GetMessage() == "" {
				t.Error("message is empty")
			}
		})
	}
}

func TestParseRateLimits(t *testing.T) {
	t.Parallel()
	h := headers(
		"X-RateLimit-Limit", "5000",
		"X-RateLimit-Remaining", "4999",
		"X-RateLimit-Reset", "1790859600",
		"Date", "Wed, 01 Oct 2026 12:00:00 GMT",
	)
	limits, providerTime := parseRateLimits(h, integrationv1.RateLimitKind_RATE_LIMIT_KIND_CORE)
	if len(limits) != 1 {
		t.Fatalf("limits = %d, want 1", len(limits))
	}
	got := limits[0]
	if got.GetLimit() != 5000 || got.GetRemaining() != 4999 {
		t.Errorf("limit,remaining = %v,%v, want 5000,4999", got.GetLimit(), got.GetRemaining())
	}
	wantReset := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	if !got.GetResetTime().AsTime().Equal(wantReset) {
		t.Errorf("reset = %v, want %v", got.GetResetTime().AsTime(), wantReset)
	}
	wantDate := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if !providerTime.Equal(wantDate) {
		t.Errorf("provider time = %v, want %v", providerTime, wantDate)
	}

	if limits, _ := parseRateLimits(headers(), integrationv1.RateLimitKind_RATE_LIMIT_KIND_CORE); len(limits) != 0 {
		// Empty headers mean no budgets reported, which the hub reads as
		// "not reported".
		t.Errorf("empty headers gave %d limits, want 0", len(limits))
	}
}

func TestPollDirective(t *testing.T) {
	t.Parallel()
	if got := pollDirective(0, 5000); got != directiveStop {
		t.Errorf("zero remaining = %v, want stop", got)
	}
	if got := pollDirective(499, 5000); got != directiveSlow {
		t.Errorf("9.98%% remaining = %v, want slow", got)
	}
	if got := pollDirective(500, 5000); got != directiveGo {
		t.Errorf("10%% remaining = %v, want go", got)
	}
	if got := pollDirective(0, 0); got != directiveGo {
		t.Errorf("unreported budget = %v, want go", got)
	}
}
