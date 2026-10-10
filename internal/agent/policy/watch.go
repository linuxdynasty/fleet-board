package policy

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher is the directory watch ([fsnotify.Watcher] behind a seam).
type Watcher interface {
	Add(dir string) error
	Remove(dir string) error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Close() error
}

type fsWatcher struct{ w *fsnotify.Watcher }

func newFSWatcher() (Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return fsWatcher{w}, nil
}

func (f fsWatcher) Add(dir string) error          { return f.w.Add(dir) }
func (f fsWatcher) Remove(dir string) error       { return f.w.Remove(dir) }
func (f fsWatcher) Events() <-chan fsnotify.Event { return f.w.Events }
func (f fsWatcher) Errors() <-chan error          { return f.w.Errors }
func (f fsWatcher) Close() error                  { return f.w.Close() }

// Watch watches the config folder until ctx ends and re-hashes policy.yaml
// after the burst of events that names it settles (500 ms by default). It
// watches the folder, not the file, because editors replace a file by
// renaming another over it. There is no timer that re-reads the file on its
// own: only an event, or a Snapshot or Settings message (see [Manager.Recheck]),
// causes a read.
//
// When the watch cannot be set (the inotify limit, no file handles) Watch
// logs policy.watch_off once, sets [Manager.WatchOff] and returns nil; it
// does not fall back to polling. Watch returns nil when ctx ends.
func (m *Manager) Watch(ctx context.Context) error {
	newWatcher := m.opts.NewWatcher
	if newWatcher == nil {
		newWatcher = newFSWatcher
	}
	w, err := newWatcher()
	if err == nil {
		if err = w.Add(m.opts.Dir); err != nil {
			_ = w.Close()
		}
	}
	if err != nil {
		m.setWatchOff(ctx, err)
		return nil
	}
	defer func() { _ = w.Close() }()
	defer m.watching.Store(false)
	// An edit between Load and the watch being set raised no event.
	m.Recheck(ctx)
	link := &linkTarget{m: m, w: w}
	link.sync(ctx)
	// Only now is the watch fully in place: a reader that sees Watching true
	// knows the first look at the file is done.
	m.watching.Store(true)

	settle := m.opts.Settle
	if settle <= 0 {
		settle = DefaultSettle
	}
	timer := time.NewTimer(settle)
	timer.Stop()
	defer timer.Stop()
	var lastWarn time.Time // policy.watch_error is logged once a minute at most
	suppressed := 0
	for {
		select {
		case <-ctx.Done():
			// A change inside the last settle window would never be looked
			// at: one bounded read of one local file, and the loader reports
			// what it finds before it closes.
			m.Recheck(context.WithoutCancel(ctx))
			return nil
		case ev, ok := <-w.Events():
			if !ok {
				return nil
			}
			if filepath.Clean(ev.Name) == filepath.Clean(m.opts.Dir) && ev.Has(fsnotify.Remove|fsnotify.Rename) {
				// The folder itself went. Look at the file now; a folder
				// put back in its place can be watched again, else the
				// watch is off (and still no poll).
				m.Recheck(ctx)
				// The old watch is dead (kqueue holds a descriptor of the
				// folder that went); drop it and watch the one in its place.
				_ = w.Remove(m.opts.Dir)
				if err := w.Add(m.opts.Dir); err != nil {
					m.setWatchOff(ctx, err)
					return nil
				}
			}
			if link.names(ev.Name) {
				timer.Reset(settle)
			}
		case err, ok := <-w.Errors():
			if !ok {
				return nil
			}
			// An overflow means events were lost; any error means we may
			// have missed one. Look at the file once after the settle.
			if now := m.now(); lastWarn.IsZero() || now.Sub(lastWarn) >= time.Minute {
				m.log.WarnContext(ctx, "policy.watch_error", "err_kind", watchErrKind(err), "suppressed", suppressed)
				lastWarn, suppressed = now, 0
			} else {
				suppressed++
			}
			timer.Reset(settle)
		case <-timer.C:
			m.Recheck(ctx)
			link.sync(ctx) // the link may have been replaced
		}
	}
}

func (m *Manager) setWatchOff(ctx context.Context, err error) {
	m.watching.Store(false)
	if !m.watchOff.Load() {
		m.log.WarnContext(ctx, "policy.watch_off", "err_kind", watchErrKind(err))
		m.watchOff.Store(true) // after the line, so a reader of the flag finds the line
	}
}

// watchErrKind sorts a watch error into the closed classes of 05b.
func watchErrKind(err error) string {
	switch {
	case errors.Is(err, fsnotify.ErrEventOverflow), errors.Is(err, syscall.ENOSPC),
		errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return "constraint"
	default:
		return "io"
	}
}
