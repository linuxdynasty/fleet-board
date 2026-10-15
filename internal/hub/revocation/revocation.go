package revocation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Options build a [List] with [Load]. Dir and Store are required.
type Options struct {
	// Dir is the state folder.
	Dir   string
	Store *store.Store
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Logger gets hub.revocations_torn_line; nil discards it.
	Logger *slog.Logger
}

// List is the hub's revocation list: the revoked-serials file, the same
// list in state.db, and the in-memory set every TLS handshake reads.
type List struct {
	dir   string
	store *store.Store
	set   atomic.Pointer[map[string]time.Time]
	mu    sync.Mutex // serializes Revoke
	dirty bool       // a file write failed; the next Revoke rewrites the file
}

// Load reads the list at start, before the agent listener binds. It removes
// a rewrite a crash left behind, reads the file (missing is normal; a loose
// mode is statedb.ErrLooseMode; a bad line or an unreadable file is
// [ErrInvalid]), drops a torn last line with a warning, and reconciles both
// ways: serials revoked only in state.db are added to the file, serials
// only in the file are marked revoked in state.db (one state.db does not
// know stays in the file only). Expired lines are dropped. When the file
// needs any change it is rewritten atomically. The set is the union.
func Load(ctx context.Context, opts Options) (*List, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	at := now()
	if err := removeTemps(opts.Dir); err != nil {
		return nil, err
	}
	p, err := readFile(filepath.Join(opts.Dir, FileName))
	if err != nil {
		return nil, err
	}
	if p.torn {
		log.WarnContext(ctx, "hub.revocations_torn_line")
	}
	stored, err := statedb.RevokedSerials(ctx, opts.Store, at)
	if err != nil {
		return nil, fmt.Errorf("revocation: %w", err)
	}
	set := make(map[string]time.Time, len(p.entries)+len(stored))
	inFile := map[string]bool{}
	expired := false
	for _, e := range p.entries {
		if !e.NotAfter.After(at) {
			expired = true
			continue
		}
		inFile[e.Serial] = true
		set[e.Serial] = later(set[e.Serial], e.NotAfter)
	}
	inStore := map[string]bool{}
	missing := false
	for _, r := range stored {
		inStore[r.Serial] = true
		missing = missing || !inFile[r.Serial]
		set[r.Serial] = later(set[r.Serial], r.NotAfter)
	}
	if p.torn || p.empty || expired || missing || (!p.exists && len(set) > 0) {
		if err := rewrite(opts.Dir, entries(set)); err != nil {
			return nil, err
		}
	}
	var toStore []string
	for s := range inFile {
		if !inStore[s] {
			toStore = append(toStore, s)
		}
	}
	if len(toStore) > 0 {
		slices.Sort(toStore)
		if _, err := statedb.RevokeSerials(ctx, opts.Store, toStore, at); err != nil {
			return nil, fmt.Errorf("revocation: %w", err)
		}
	}
	l := &List{dir: opts.Dir, store: opts.Store}
	l.set.Store(&set)
	return l, nil
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func entries(set map[string]time.Time) []Entry {
	out := make([]Entry, 0, len(set))
	for s, t := range set {
		out = append(out, Entry{Serial: s, NotAfter: t})
	}
	return out
}

// Revoked reports whether serial (lowercase hex) is on the list. It is safe
// for concurrent use and never blocks: it reads an atomic pointer.
func (l *List) Revoked(serial string) bool {
	_, ok := (*l.set.Load())[serial]
	return ok
}

// Stage appends entries missing from the in-memory set to the file and
// the set (the first two steps of the 02g write order), without the
// state.db write. The caller submits the store write next; a store
// failure leaves the serials revoked (fail closed) and the next start
// reconciles state.db from the file. Entries already revoked are
// skipped, so a retry adds no duplicate lines.
func (l *List) Stage(add []Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	set := *l.set.Load()
	var missing []Entry
	for _, e := range add {
		if !serialHex.MatchString(e.Serial) {
			return errors.New("revocation: a serial is not lowercase hex")
		}
		if _, ok := set[e.Serial]; ok {
			continue
		}
		missing = append(missing, e)
	}
	if len(missing) == 0 {
		return nil
	}
	next := maps.Clone(set)
	for _, e := range missing {
		next[e.Serial] = later(next[e.Serial], e.NotAfter)
	}
	var err error
	if l.dirty {
		err = rewrite(l.dir, entries(next))
	} else {
		err = appendEntries(l.dir, missing)
	}
	if err != nil {
		l.dirty = true
		return err
	}
	l.dirty = false
	l.set.Store(&next)
	return nil
}

// Revoke adds entries in the 02g order: append and fsync the file (fsync
// the folder when the file is new), then the in-memory set, then the
// state.db write. A file failure changes nothing else and is returned; the
// next Revoke rewrites the whole file, so a torn append is not built on. A
// state.db failure is returned with the serials already revoked in the
// file and the set: revocation fails closed, and the next start reconciles
// state.db from the file.
func (l *List) Revoke(ctx context.Context, add []Entry, at time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	next := maps.Clone(*l.set.Load())
	serials := make([]string, 0, len(add))
	for _, e := range add {
		if !serialHex.MatchString(e.Serial) {
			return errors.New("revocation: a serial is not lowercase hex")
		}
		next[e.Serial] = later(next[e.Serial], e.NotAfter)
		serials = append(serials, e.Serial)
	}
	var err error
	if l.dirty {
		err = rewrite(l.dir, entries(next))
	} else {
		err = appendEntries(l.dir, add)
	}
	if err != nil {
		l.dirty = true
		return err
	}
	l.dirty = false
	l.set.Store(&next)
	if _, err := statedb.RevokeSerials(ctx, l.store, serials, at); err != nil {
		return fmt.Errorf("revocation: revoked in %s and in memory, not yet in state.db: %w", FileName, err)
	}
	return nil
}
