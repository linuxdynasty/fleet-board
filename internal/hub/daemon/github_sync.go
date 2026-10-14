package daemon

import (
	"context"
	"fmt"
	"os"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	github "github.com/linuxdynasty/fleet-board/internal/hub/integration/github"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/hub/syncer"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// GitHubIntegrationID is the stable id of the Integration row the
// `github:` keys of fleetd.yaml create at start (02j). One hub watches
// through one App installation in v1, so the row id is fixed; mirrors
// stay joined to it across restarts.
const GitHubIntegrationID = "github"

// githubSyncer builds the row-11 sync loop when fleetd.yaml holds github
// keys, else nil. The App private key is read here (never stored): the
// file must be owner-only, and a loose or missing key refuses the start
// like any other credential the hub would otherwise leak. The trusted
// gate starts empty — the default label with nobody listed, so nothing
// auto-dispatches until row 13a manages the allow-list.
func githubSyncer(ctx context.Context, cfg *config.Config, st *store.Store, tel *telemetry.Telemetry, now time.Time) (*syncer.Syncer, error) {
	gh := cfg.GitHub
	if gh == nil {
		return nil, nil
	}
	key, err := readOwnerOnly(gh.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	src, err := github.NewTokenSource(nil, "https://api.github.com", gh.AppID, gh.InstallationID, key)
	if err != nil {
		return nil, fmt.Errorf("daemon: github app key: %w", err)
	}
	if err := statedb.EnsureIntegration(ctx, st, statedb.IntegrationRow{
		ID: GitHubIntegrationID, Provider: "github",
		BaseURL: "https://github.com", Label: "github",
	}, now.UnixMilli()); err != nil {
		return nil, fmt.Errorf("daemon: github integration row: %w", err)
	}
	metrics, err := syncer.NewMetrics(tel)
	if err != nil {
		return nil, fmt.Errorf("daemon: github sync metrics: %w", err)
	}
	sy := syncer.New(st, map[string]syncer.Adapter{
		GitHubIntegrationID: &github.Adapter{
			C:           &github.Client{Tokens: src},
			Integration: GitHubIntegrationID,
		},
	}, &hubv1.TrustedGate{}, tel.Logger, metrics)
	sy.OnTriage = func(n syncer.TriageNotice) {
		if n.Retire {
			tel.Logger.InfoContext(ctx, "sync.triage_retired", "kind", n.Kind,
				"integration", n.Integration, "project", n.Project, "fingerprint", n.Fingerprint)
			return
		}
		// The triage_items write lands with row 12; until then the
		// fingerprint carries the 03d dedupe key in the log.
		tel.Logger.WarnContext(ctx, "sync.triage_raised", "kind", n.Kind,
			"integration", n.Integration, "project", n.Project,
			"fingerprint", n.Fingerprint, "detail", n.Detail)
	}
	return sy, nil
}

// readOwnerOnly reads a credential file the owner keeps at mode 0600
// (02j). Anything readable by group or others refuses, naming the file
// but never its contents.
func readOwnerOnly(path string) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("daemon: read %s: %w", path, err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("daemon: %s is mode %04o, want owner-only (chmod 600 %s)", path, perm, path)
	}
	body, err := os.ReadFile(path) //nolint:gosec // G304: path is the operator's own configured key file; reading it is the job
	if err != nil {
		return nil, fmt.Errorf("daemon: read %s: %w", path, err)
	}
	return body, nil
}
