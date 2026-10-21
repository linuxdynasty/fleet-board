package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/housekeeping"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/hub/rollup"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// InsightsRollupJob is the hourly subscription-efficiency rollup job
// fleetd run registers (05 section 2): today and yesterday recomputed
// from the stored readings, once an hour. A failed pass is counted by
// the rollup and retried next hour.
func InsightsRollupJob(roller *rollup.Roller, st *store.Store) housekeeping.Job {
	return housekeeping.Job{
		Name: "insights_rollup", Every: time.Hour, Run: func(ctx context.Context, now time.Time) error {
			_, err := roller.Rollup(ctx, st, now)
			return err
		},
	}
}

// InsightsRebuildRequest is what `fleetd insights rebuild` asks for: the
// UTC day the recompute starts at.
type InsightsRebuildRequest struct {
	// From is the first day to recompute; windows ending before it are
	// left untouched.
	From time.Time
}

// InsightsRebuild is `fleetd insights rebuild --from <date>`, an offline
// command: it reads fleetd.yaml, takes fleetd.lock and holds it to the
// end, so it is refused while fleetd run holds it (#92: lock_held), and
// recomputes the subscription-efficiency rollup rows ending in [from,
// now] from the full reading history. Older stored windows are
// untouched, so a gap heals without rewriting history.
func InsightsRebuild(ctx context.Context, opts Options, req InsightsRebuildRequest) (res rollup.Result, err error) {
	s, err := newSession("insights-rebuild", opts)
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, s.close()) }()
	cfg, err := config.Load(opts.ConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("%s does not exist; write it (fleetd init writes the default) and rebuild again: %w", opts.ConfigPath, err)
	}
	if err != nil {
		return res, s.fail(ReasonConfig, err)
	}
	if err := statedb.EnsureDir(cfg.StateDir); err != nil {
		return res, s.fail(dirReason(err), err)
	}
	l, err := lock.Acquire(filepath.Join(cfg.StateDir, LockName))
	if errors.Is(err, lock.ErrHeld) {
		err = fmt.Errorf("%s: %w; stop fleetd run before rebuilding", cfg.StateDir, err)
	}
	if err != nil {
		return res, s.fail(lockReason(err), err)
	}
	defer func() { err = errors.Join(err, l.Release()) }()
	st, err := statedb.Open(ctx, cfg.StateDir, s.tel)
	if err != nil {
		return res, s.fail(ReasonStore, err)
	}
	defer func() { err = errors.Join(err, st.Close()) }()
	now := opts.now()
	roller, err := rollup.New(rollup.Options{Now: opts.Now, Telemetry: s.tel})
	if err != nil {
		return res, s.fail(ReasonStore, err)
	}
	res, err = roller.Rebuild(ctx, st, req.From.UTC(), now)
	if err != nil {
		return res, s.fail(ReasonStore, err)
	}
	s.tel.Logger.InfoContext(ctx, "insights.rebuilt",
		"from", req.From.UTC().Format(time.DateOnly), "windows", res.Windows, "accounts", res.Accounts)
	return res, nil
}
