package backup

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// Schedule constants (defaults.md "Backup", ADR 0024 decision 16).
const (
	// Hour is when the nightly copy is due, in hub local time.
	Hour = 3
	// Keep is how many nightly copies are kept; manual ones are never pruned.
	Keep = 7
	// CheckEvery is how often fleetd run checks whether a copy is due.
	CheckEvery = time.Minute
	// catchUpAfter is how old the newest nightly copy may be before a start
	// or a wake makes one at once.
	catchUpAfter = 24 * time.Hour
	// wakeGap is a gap between two checks that means the hub was asleep.
	wakeGap = 3 * CheckEvery
)

const scope = "github.com/linuxdynasty/fleet-board/internal/hub/backup"

// ScheduleOptions configure [NewSchedule].
type ScheduleOptions struct {
	StateDir string
	// Now is the clock; its location is the hub's local time. nil means
	// time.Now (time.Local).
	Now func() time.Time
	// Telemetry receives the backup log events and serves
	// fleet_hub_backup_age_seconds; nil turns both off.
	Telemetry *telemetry.Telemetry

	seams seams
}

// Schedule is the nightly job inside fleetd run. Only the goroutine that
// calls Check (or Run) uses it.
type Schedule struct {
	dir    string
	now    func() time.Time
	tel    *telemetry.Telemetry
	log    *slog.Logger
	gauge  metric.Registration
	last   time.Time // the clock at the previous Check; zero before the first
	tried  string    // the local date whose 03:00 copy was already tried
	warned time.Time // the stamp backup.stamp_ahead last reported
	seams  seams
}

// NewSchedule builds the job and registers fleet_hub_backup_age_seconds: the
// age of the newest backup of either kind, read from the stamp in its name
// at scrape time, and no value at all while there is no backup (BACKUP_AGE
// then reads unknown). Close unregisters it.
func NewSchedule(opts ScheduleOptions) (*Schedule, error) {
	s := &Schedule{dir: opts.StateDir, now: opts.Now, tel: opts.Telemetry, log: logger(opts.Telemetry), seams: opts.seams}
	if s.now == nil {
		s.now = time.Now
	}
	if s.tel == nil {
		return s, nil
	}
	m := s.tel.Meter(scope)
	age, err := m.Float64ObservableGauge(s.tel.MetricPrefix()+"_backup_age_seconds",
		metric.WithDescription("Age of the newest backup in backups/, nightly or manual; no value while there is none."),
		metric.WithUnit("s"))
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.dir, DirName)
	s.gauge, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		now := s.now()
		files, err := list(dir)
		files, _ = current(files, now)
		if t := newest(files, ""); err == nil && !t.IsZero() {
			o.ObserveFloat64(age, max(now.Sub(t).Seconds(), 0)) // up to aheadLimit ahead reads 0
		}
		return nil
	}, age)
	return s, err
}

// Close unregisters the gauge.
func (s *Schedule) Close() error {
	if s.gauge == nil {
		return nil
	}
	return s.gauge.Unregister()
}

// Run checks at once, then at every tick, until ctx ends.
func (s *Schedule) Run(ctx context.Context, ticks <-chan time.Time) {
	s.Check(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.Check(ctx)
		}
	}
}

// Check makes a nightly copy when one is due, then prunes to the last
// [Keep]. One is due at the first check of a local date at or after 03:00
// when no nightly copy is that new yet (tried once per date, even if it
// fails), and at the first check after a start or a wake (a gap of more than
// three check intervals) when the newest nightly copy is more than 24 h old.
// Failures are logged (backup.failed) and wait for the next due time.
func (s *Schedule) Check(ctx context.Context) {
	now := s.now()
	wake := s.last.IsZero() || now.Round(0).Sub(s.last.Round(0)) > wakeGap
	s.last = now
	dir := filepath.Join(s.dir, DirName)
	files, err := list(dir) // an unreadable folder counts as no copy; Take then logs why
	files, ahead := current(files, now)
	s.warnAhead(ahead, now)
	last := newest(files, Nightly)
	if err != nil {
		last = time.Time{}
	}
	due := false
	slot := slotOf(now)
	if day := now.Format(time.DateOnly); !now.Before(slot) && last.Before(slot) && s.tried != day {
		s.tried, due = day, true
	}
	if wake && (last.IsZero() || now.Sub(last) > catchUpAfter) {
		due = true
	}
	if !due {
		return
	}
	res, err := Take(ctx, Options{StateDir: s.dir, Trigger: Nightly, Now: s.now, Telemetry: s.tel, seams: s.seams})
	if err != nil && res.Path == "" {
		return // Take logged backup.failed; nothing was published
	}
	if err := prune(dir, Keep, s.seams.remove); err != nil {
		s.log.Error("backup.failed", "trigger", string(Nightly), "step", string(stepPrune), "err_kind", errKind(err))
	}
}

// slotOf is the first instant of t's local date whose wall clock reads
// Hour:00 or later: on a spring-forward night that skips Hour:00, the first
// instant after the jump; on a fall-back night that repeats it, the first
// of the two. time.Date promises neither, so this walks from local midnight
// in 15-minute steps (every zone's offset changes are multiples of 15
// minutes, so the walk lands on Hour:00 whenever it exists).
func slotOf(t time.Time) time.Time {
	s := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	for s.YearDay() != t.YearDay() || s.Hour() < Hour {
		s = s.Add(15 * time.Minute)
	}
	return s
}

// warnAhead logs backup.stamp_ahead when the newest copy stamped past
// aheadLimit changes, so a wrong clock is reported once, not every minute.
func (s *Schedule) warnAhead(ahead []file, now time.Time) {
	if len(ahead) == 0 {
		return
	}
	at := ahead[len(ahead)-1].at
	if at.Equal(s.warned) {
		return
	}
	s.warned = at
	s.log.Warn("backup.stamp_ahead", "files", len(ahead), "ahead_seconds", int64(at.Sub(now).Seconds()))
}
