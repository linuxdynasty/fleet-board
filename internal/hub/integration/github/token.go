package github

import (
	"context"
	"fmt"
	"net/http"

	ghinstallation "github.com/bradleyfalzon/ghinstallation/v2"
)

// TokenSource mints GitHub App installation tokens (ADR 0005). The token
// is cached and refreshed before expiry inside; callers put it on the
// Authorization header of REST and GraphQL calls. The private key never
// leaves this package's transport.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// NewTokenSource builds the installation-token source per ADR 0020:
// ghinstallation mints installation tokens only, and the adapter's own
// typed wrapper over net/http makes the API calls (google/go-github is
// not used: it releases a new major every few weeks and the adapter must
// control ETags and rate-limit headers itself). apiBase is
// https://api.github.com in production; tests point it at httptest.
func NewTokenSource(base http.RoundTripper, apiBase string, appID, installationID int64, privateKey []byte) (TokenSource, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	atr, err := ghinstallation.NewAppsTransport(base, appID, privateKey)
	if err != nil {
		return nil, fmt.Errorf("github: app transport: %w", err)
	}
	tr := ghinstallation.NewFromAppsTransport(atr, installationID)
	if apiBase != "" {
		tr.BaseURL = apiBase
	}
	return tr, nil
}

// TokenSourceFunc adapts a function to a TokenSource (fakes in syncer
// tests serve fixed tokens without HTTP).
type TokenSourceFunc func(ctx context.Context) (string, error)

// Token calls the function.
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }
