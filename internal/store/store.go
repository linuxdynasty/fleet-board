package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/telemetry"
	_ "modernc.org/sqlite" // the "sqlite" driver: pure Go, ADR 0014
)

// Limits and defaults from ADR 0014 and defaults.md.
const (
	MaxBatch           = 100
	DefaultBatchWindow = 250 * time.Millisecond
	MaxReaders         = 10
	// DefaultQueueSize bounds the writer queue at ten full batches; a full
	// queue makes Submit wait (backpressure, 03f).
	DefaultQueueSize = 10 * MaxBatch
)

var (
	// ErrClosed is returned by Submit, Write and Read after Close.
	ErrClosed = errors.New("store: closed")
	// ErrSchemaTooNew means the file was migrated by a newer binary.
	ErrSchemaTooNew = errors.New("store: database schema is newer than this binary")
	// ErrMigrationChanged means an applied migration's SQL was edited.
	ErrMigrationChanged = errors.New("store: an applied migration was changed")
	// ErrMissing means Options.MustExist was set and the file does not exist.
	ErrMissing = errors.New("store: database file does not exist")
)

// Options configure [Open]. Zero values take the ADR 0014 defaults.
type Options struct {
	// Path is the database file; it must not contain '?'.
	Path string
	// Name is the file's role and the db label on every metric: "state",
	// "logs" or "agent".
	Name string
	// Migrations are applied in order at Open; see [Migration].
	Migrations []Migration
	// MustExist opens the file read-write without ever creating it (SQLite's
	// mode=rw): a missing file is [ErrMissing] and nothing is written.
	MustExist bool
	// BatchMax is the most jobs in one transaction (default and limit 100).
	BatchMax int
	// BatchWindow is how long the first job of a batch waits for others
	// (default 250 ms).
	BatchWindow time.Duration
	// Readers is the read pool size (default and limit 10).
	Readers int
	// QueueSize bounds the writer queue (default 1,000).
	QueueSize int
	// Telemetry receives metrics and log lines; nil turns both off.
	Telemetry *telemetry.Telemetry
}

// Store is one SQLite file with a single writer goroutine and a read pool.
type Store struct {
	name    string
	path    string
	max     int
	window  time.Duration
	version int
	log     *slog.Logger
	m       *metrics

	writer  *sql.DB // one connection, BEGIN IMMEDIATE
	readers *sql.DB // query_only connections

	mu      sync.RWMutex // guards closed and the send on jobs
	closed  bool
	jobs    chan job
	stopped chan struct{}
	wake    chan struct{} // writer-owned, coalesced hint after successful commit
	// oldest is the Submit time (Unix ns) of the first job of the batch the
	// writer holds, 0 when it holds none. Jobs are FIFO, so no queued job is
	// older.
	oldest atomic.Int64
	// fullSince is when the queue became continuously full (Unix ns), 0 when
	// it is not full. Set by a Submit that finds no room; cleared by the
	// writer when it takes a job and the queue has room. The
	// WRITER_QUEUE_FULL measure of 05b reads it through QueueFullSince.
	fullSince atomic.Int64
	// hook runs on the writer goroutine after each committed batch's own
	// hooks, in commit order (#393). Set once via OnCommit before jobs
	// that need it commit; unset it runs nothing.
	hook atomic.Value
	// audits is the per-job audit publish function SetAuditPublisher
	// records (as any; see writer.go). Only the writer reads it, inside
	// job hooks.
	audits atomic.Value

	closeOnce sync.Once
	closeErr  error
}

var names = map[string]bool{"state": true, "logs": true, "agent": true}

func (o *Options) validate() error {
	switch {
	case o.Path == "":
		return errors.New("store: Path is required")
	case strings.ContainsRune(o.Path, '?'):
		return errors.New("store: Path must not contain '?'")
	case !names[o.Name]:
		return fmt.Errorf("store: Name %q is not state, logs or agent", o.Name)
	case o.BatchMax < 0 || o.BatchMax > MaxBatch:
		return fmt.Errorf("store: BatchMax %d is outside 1..%d", o.BatchMax, MaxBatch)
	case o.BatchWindow < 0:
		return errors.New("store: BatchWindow is negative")
	case o.Readers < 0 || o.Readers > MaxReaders:
		return fmt.Errorf("store: Readers %d is outside 1..%d", o.Readers, MaxReaders)
	case o.QueueSize < 0:
		return errors.New("store: QueueSize is negative")
	}
	if o.BatchMax == 0 {
		o.BatchMax = MaxBatch
	}
	if o.BatchWindow == 0 {
		o.BatchWindow = DefaultBatchWindow
	}
	if o.Readers == 0 {
		o.Readers = MaxReaders
	}
	if o.QueueSize == 0 {
		o.QueueSize = DefaultQueueSize
	}
	return validateMigrations(o.Migrations)
}

// dsn sets the ADR 0014 pragmas on every connection the pool opens. The
// writer of logs.db also sets auto_vacuum=INCREMENTAL (ADR 0015); it is first
// because SQLite ignores it once the file has a table. mustExist names the
// file as an escaped file: URI with mode=rw, the only form in which the
// driver passes SQLite's own mode parameter through.
func dsn(path, name string, reader, mustExist bool) string {
	var p []string
	if mustExist {
		path = (&url.URL{Scheme: "file", Path: path}).String()
		p = append(p, "mode=rw")
	}
	if name == "logs" && !reader {
		p = append(p, "_pragma=auto_vacuum(2)")
	}
	p = append(p,
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(FULL)",
		"_pragma=fullfsync(1)",
		"_pragma=busy_timeout(5000)",
		"_pragma=foreign_keys(1)",
		"_pragma=journal_size_limit(67108864)",
	)
	if reader {
		p = append(p, "_pragma=query_only(1)")
	} else {
		p = append(p, "_txlock=immediate")
	}
	return path + "?" + strings.Join(p, "&")
}

// Open opens or creates the file, applies pending migrations on the writer
// connection, opens the read pool and starts the writer goroutine.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	s := &Store{
		name:    opts.Name,
		path:    opts.Path,
		max:     opts.BatchMax,
		window:  opts.BatchWindow,
		log:     slog.New(slog.DiscardHandler),
		jobs:    make(chan job, opts.QueueSize),
		stopped: make(chan struct{}),
		wake:    make(chan struct{}, 1),
	}
	if opts.Telemetry != nil {
		s.log = opts.Telemetry.Logger.With("component", "store", "db", opts.Name)
	}
	w, err := sql.Open("sqlite", dsn(opts.Path, opts.Name, false, opts.MustExist))
	if err != nil {
		return nil, fmt.Errorf("store: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)
	s.writer = w
	var mode string
	if err := w.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		if _, statErr := os.Stat(opts.Path); opts.MustExist && errors.Is(statErr, fs.ErrNotExist) {
			err = ErrMissing
		}
		return nil, errors.Join(fmt.Errorf("store: open %s: %w", opts.Name, err), w.Close())
	}
	if mode != "wal" {
		return nil, errors.Join(fmt.Errorf("store: journal_mode is %q, want wal", mode), w.Close())
	}
	if s.version, err = migrate(ctx, w, opts.Migrations, s.log); err != nil {
		return nil, errors.Join(err, w.Close())
	}
	r, err := sql.Open("sqlite", dsn(opts.Path, opts.Name, true, opts.MustExist))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("store: open readers: %w", err), w.Close())
	}
	r.SetMaxOpenConns(opts.Readers)
	r.SetMaxIdleConns(opts.Readers)
	s.readers = r
	obs := observed{path: opts.Path, depth: func() int { return len(s.jobs) }, oldest: s.oldestJob, fullSince: s.QueueFullSince}
	if s.m, err = newMetrics(opts.Telemetry, opts.Name, obs); err != nil {
		return nil, errors.Join(err, r.Close(), w.Close())
	}
	go s.run()
	s.log.Info("store.opened", "schema_version", s.version)
	return s, nil
}

// SchemaVersion is the highest migration applied to the file.
func (s *Store) SchemaVersion() int { return s.version }

// Close stops accepting jobs, commits every job already queued, waits for
// the writer to stop and closes both pools. A Read already running keeps its
// connection and finishes after Close returns. It is safe to call twice.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.jobs)
		s.mu.Unlock()
		<-s.stopped
		s.closeErr = errors.Join(s.m.close(), s.readers.Close(), s.writer.Close())
		s.log.Info("store.closed")
	})
	return s.closeErr
}
