package github

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

// maxErrorMessage is the ProviderError.message cap (sync.proto: 512 bytes of
// untrusted text, which may echo the provider).
const maxErrorMessage = 512

// classifyStatus maps an HTTP response to a ProviderError (03j table), so
// the syncer sets health and back-off without parsing text. project names
// the repo or project the call concerned; op names the call for the message
// ("GET /repos/o/r/issues"); detail carries the short reason. A 304 never
// reaches here: the caller treats it as not_modified.
func classifyStatus(status int, header http.Header, project, op, detail, body string) *integrationv1.ProviderError {
	msg := op
	if detail != "" {
		msg += ": " + detail
	}
	if body != "" {
		msg += " (" + body + ")"
	}
	err := &integrationv1.ProviderError{
		Project:        project,
		ProviderStatus: fmt.Sprintf("HTTP %d", status),
		Message:        truncate(msg, maxErrorMessage),
	}
	switch {
	case status == http.StatusNotFound:
		err.Kind = integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_NOT_FOUND
	case status == http.StatusUnauthorized:
		err.Kind = integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAUTHENTICATED
	case status == http.StatusForbidden && rateLimitedResponse(header, body):
		setRateLimited(err, header)
	case status == http.StatusTooManyRequests:
		setRateLimited(err, header)
	case status == http.StatusForbidden:
		err.Kind = integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_FORBIDDEN
	case status >= 500:
		err.Kind = integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNAVAILABLE
		err.Retryable = true
	default:
		err.Kind = integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED
	}
	return err
}

// setRateLimited fills a RATE_LIMITED error from Retry-After or the rate
// limit reset the headers report (03j: retry after retry_after or reset).
func setRateLimited(err *integrationv1.ProviderError, header http.Header) {
	err.Kind = integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_RATE_LIMITED
	err.Retryable = true
	limits, _ := parseRateLimits(header, integrationv1.RateLimitKind_RATE_LIMIT_KIND_CORE)
	if len(limits) > 0 {
		err.RateLimit = limits[0]
	}
	if after, ok := retryAfter(header); ok {
		err.RetryAfter = durationpb.New(after)
		return
	}
	if len(limits) > 0 && limits[0].GetResetTime() != nil {
		if wait := time.Until(limits[0].GetResetTime().AsTime()); wait > 0 {
			err.RetryAfter = durationpb.New(wait)
		}
	}
}

// rateLimitedResponse tells an exhausted budget (403/429) from a refused
// permission (403): zero remaining, a Retry-After, or the body's own words.
func rateLimitedResponse(header http.Header, body string) bool {
	if header.Get("Retry-After") != "" {
		return true
	}
	if rem := header.Get("X-RateLimit-Remaining"); rem != "" {
		if n, convErr := strconv.Atoi(strings.TrimSpace(rem)); convErr == nil && n == 0 {
			return true
		}
	}
	return strings.Contains(strings.ToLower(body), "rate limit")
}

// retryAfter reads Retry-After in seconds (Retry-After: HTTP date is rare
// from GitHub and is ignored).
func retryAfter(header http.Header) (time.Duration, bool) {
	raw := strings.TrimSpace(header.Get("Retry-After"))
	if raw == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(raw)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// truncate caps untrusted text at n bytes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// graphQLError maps one GraphQL error object to a ProviderError. A
// RATE_LIMITED type backs off like a 429; anything else is a hub bug
// (UNSUPPORTED): the query was built by the adapter, not the provider.
func graphQLError(errType, message, project string) *integrationv1.ProviderError {
	err := &integrationv1.ProviderError{
		Project:        project,
		ProviderStatus: "GraphQL " + errType,
		Message:        truncate(message, maxErrorMessage),
	}
	if errType == "RATE_LIMITED" {
		err.Kind = integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_RATE_LIMITED
		err.Retryable = true
		return err
	}
	err.Kind = integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED
	return err
}
