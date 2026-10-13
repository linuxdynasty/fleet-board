// Package github syncs watched repositories with GitHub through a GitHub
// App installation (docs/specs/03m-work-source-and-code-host-sync.md,
// ADR 0005, ADR 0020).
//
// The adapter is read-only and keeps no sync state: each call takes the
// cursor and validators the hub stored and returns the next ones, the rate
// limits and a typed ProviderError, so the hub owns polling, back-off and
// health (03j). Issues come from REST with ETags (`If-None-Match`: a 304
// answer is free and counts nothing); change requests and timeline evidence
// come from batched GraphQL; the installation token is minted from the
// App's private key and refreshed before expiry. There is no fine-grained
// token fallback in v1 (decision 15 of ADR 0024).
package github
