package inventory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// SocketFolder is the folder tmux keeps its sockets in, by tmux's own rule:
// $TMUX_TMPDIR, else /tmp, then tmux-<uid> (row-7 ruling fork 4(a)). The
// agent's environment names it; a server started with -S elsewhere is not
// seen (01 E2).
func SocketFolder(getenv func(string) string, uid int) string {
	base := getenv("TMUX_TMPDIR")
	if base == "" {
		base = "/tmp"
	}
	return filepath.Join(base, "tmux-"+strconv.Itoa(uid))
}

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

// wakeGap is the least time between two Wakes of one client: each Wake of
// an absent client costs one `tmux list-sessions` (internal/agent/tmux
// README), and a busy folder can change many times a second.
const wakeGap = time.Second

// client is what the watch needs of a tmux client.
type client interface {
	Wake()
	Up() bool
}

// waker wakes one client for the watch: at once, or, inside wakeGap of
// the last Wake, once at the end of the gap. A client that is up is not
// woken (a repeat Create for a socket it is attached to is a no-op).
type waker struct {
	c   client
	now func() time.Time

	mu      sync.Mutex
	last    time.Time
	pending bool
}

func (w *waker) poke() {
	if w.c.Up() {
		return
	}
	w.mu.Lock()
	now := w.now()
	if w.pending {
		w.mu.Unlock()
		return
	}
	if wait := wakeGap - now.Sub(w.last); !w.last.IsZero() && wait > 0 {
		w.pending = true
		w.mu.Unlock()
		time.AfterFunc(wait, w.fire)
		return
	}
	w.last = now
	w.mu.Unlock()
	w.c.Wake()
}

func (w *waker) fire() {
	w.mu.Lock()
	w.pending, w.last = false, w.now()
	w.mu.Unlock()
	if !w.c.Up() {
		w.c.Wake()
	}
}

// ensureFolder makes the socket folder as tmux would when it is missing
// (0700, this user's), so it can be watched before the first server.
func ensureFolder(dir string) error {
	err := os.Mkdir(dir, 0o700)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	return err
}

// watch watches the socket folder until ctx ends. An event naming a
// server's socket pokes that server's waker; a token on o.rescan (a
// Snapshot, a SnapshotRequest, a control client's %exit) re-lists the
// folder and pokes each server whose socket is there. There is no timer
// (row-7 ruling fork 4(b)): a watch that cannot be set logs tmux.watch_off
// once and the rescans are all that is left.
func (o *Observer) watch(ctx context.Context) {
	newWatcher := o.opts.NewWatcher
	if newWatcher == nil {
		newWatcher = newFSWatcher
	}
	var events <-chan fsnotify.Event
	var errs <-chan error
	w, err := newWatcher()
	if err == nil {
		if err = ensureFolder(o.folder); err == nil {
			err = w.Add(o.folder)
		}
		if err != nil {
			_ = w.Close()
		}
	}
	if err != nil {
		o.tel.watchOff(ctx, watchErrKind(err))
	} else {
		defer func() { _ = w.Close() }()
		events, errs = w.Events(), w.Errors()
	}
	o.rescanFolder() // a socket made before the watch was set raised no event
	for {
		select {
		case <-ctx.Done():
			return
		case <-o.rescan:
			o.rescanFolder()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if filepath.Clean(ev.Name) == filepath.Clean(o.folder) && ev.Has(fsnotify.Remove|fsnotify.Rename) {
				// The folder itself went (kqueue's watch dies with it):
				// make it again and watch the new one, or the watch is off.
				_ = w.Remove(o.folder)
				if err := ensureFolder(o.folder); err != nil || w.Add(o.folder) != nil {
					o.tel.watchOff(ctx, "io")
					events, errs = nil, nil
				}
				continue
			}
			if s := o.servers[filepath.Base(ev.Name)]; s != nil && filepath.Dir(filepath.Clean(ev.Name)) == filepath.Clean(o.folder) {
				s.wake.poke()
			}
		case _, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			o.rescanFolder() // an overflow may have lost an event
		}
	}
}

// rescanFolder pokes each server whose socket is in the folder.
func (o *Observer) rescanFolder() {
	for name, s := range o.servers {
		if fi, err := os.Lstat(filepath.Join(o.folder, name)); err == nil && fi.Mode()&os.ModeSocket != 0 {
			s.wake.poke()
		}
	}
}

// askRescan asks the watch goroutine to re-list the folder; it never
// blocks (a token already waiting covers this one).
func (o *Observer) askRescan() {
	select {
	case o.rescan <- struct{}{}:
	default:
	}
}

// watchErrKind sorts a watch error into the closed classes of 05b.
func watchErrKind(err error) string {
	switch {
	case errors.Is(err, fsnotify.ErrEventOverflow), errors.Is(err, syscall.ENOSPC),
		errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE):
		return "constraint"
	case errors.Is(err, os.ErrPermission):
		return "permission"
	default:
		return "io"
	}
}
