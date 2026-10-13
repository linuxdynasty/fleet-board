# internal/hub/integration/github

GitHub as a work tracker and code host (03m W4), read-only through a GitHub
App installation (ADR 0005).

- `errors.go`: HTTP status and GraphQL errors become `ProviderError`
  (03j table): 404 is `NOT_FOUND`, 401 `UNAUTHENTICATED`, 403/429 with an
  exhausted budget `RATE_LIMITED`, other 403s `FORBIDDEN`, 5xx and network
  failures `UNAVAILABLE`, stale cursors `CURSOR_EXPIRED`.
- `ratelimit.go`: `x-ratelimit-*` and GraphQL `rateLimit` become
  `RateLimitState`; `Retry-After` and the `Date` header become `retry_after`
  and `provider_time`. The hub slows polling four times under 10 %
  remaining and sleeps to `reset_time` at zero (03j).
- `token.go`: the installation token from the App private key, cached and
  refreshed before expiry; the key file stays mode 0600 and never enters
  the store or a log.
- Issues (REST, ETagged) and change requests plus timeline evidence
  (GraphQL, batched) arrive in later slices of row 11.

Tests: `CGO_ENABLED=0 go test ./internal/hub/integration/github/`.
