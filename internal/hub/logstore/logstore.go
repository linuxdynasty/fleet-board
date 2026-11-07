package logstore

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// ensureFile creates the database file mode 0600 when missing (the umask
// can only narrow that) and refuses an existing file with group or other
// access: terminal output can hold secrets.
func ensureFile(path string) error {
	// Path is dir/logs.db from the hub's configured state folder.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // G304: path is dir/logs.db, never hub text
	if err != nil {
		return fmt.Errorf("logstore: create logs.db: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("logstore: create logs.db: %w", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("logstore: stat logs.db: %w", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("logstore: logs.db is mode %o, want owner-only: %w", fi.Mode().Perm(), fs.ErrPermission)
	}
	return nil
}

// Stream identities, matching fleet.type.v1 LogStream values.
const (
	TerminalStream   = 1
	TranscriptStream = 2
)

// Pane names one tmux pane: the agent wire's (pane, stream) identity.
type Pane struct {
	Server string
	Start  string
	ID     string
}

// String is the storage form. \x1f cannot appear in a pane id (%N), a
// server name ([a-z0-9-]) or the agent-minted start id, so the split is
// lossless; ParsePane inverts it.
func (p Pane) String() string {
	return p.Server + "\x1f" + p.Start + "\x1f" + p.ID
}

// ParsePane inverts [Pane.String].
func ParsePane(s string) (Pane, error) {
	var p Pane
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1f {
			n++
		}
	}
	if n != 2 {
		return p, fmt.Errorf("logstore: pane %q is not server\\x1fstart\\x1fid", s)
	}
	parts := [3]string{}
	idx := 0
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1f {
			parts[idx] = s[start:i]
			idx++
			start = i + 1
		}
	}
	parts[2] = s[start:]
	p = Pane{Server: parts[0], Start: parts[1], ID: parts[2]}
	if p.Server == "" || p.Start == "" || p.ID == "" {
		return p, fmt.Errorf("logstore: pane %q has an empty element", s)
	}
	return p, nil
}

// Chunk is one compressed span of a stream's raw bytes, as the agent sent
// it: the zstd blocks of the frame that starts at FrameRawOffset. Offsets
// count raw bytes from the start of capture; lost bytes still take up
// offsets (GapBefore).
type Chunk struct {
	Session string
	Machine string
	Pane    Pane
	Stream  int
	// Offset of the first raw byte in this chunk.
	Offset uint64
	// Data holds the zstd blocks as received, at most 960 KiB.
	Data           []byte
	RawLength      uint32
	GapBefore      uint64
	FirstTime      time.Time
	LastTime       time.Time
	Final          bool
	Goal           string
	FrameStart     bool
	FrameRawOffset uint64
}

// Verdict is the hub's answer to one chunk: the status LogAck carries.
type Verdict int

const (
	// Stored means the row landed and the end moved.
	Stored Verdict = iota + 1
	// Duplicate means the bytes were already stored; the end is re-acked.
	Duplicate
	// Hole means offset - gap_before != stored end and the chunk stored
	// nothing; the agent resends from the returned end.
	Hole
	// UnknownStream means the pane maps to no session yet; the agent keeps
	// the spool and retries after the next snapshot.
	UnknownStream
)

func (v Verdict) String() string {
	switch v {
	case Stored:
		return "STORED"
	case Duplicate:
		return "DUPLICATE"
	case Hole:
		return "HOLE"
	case UnknownStream:
		return "UNKNOWN_STREAM"
	}
	return "UNKNOWN"
}

// Resolver maps a pane to the hub SessionId and machine the hub assigned.
// False means the hub knows no session for the pane yet.
type Resolver func(p Pane) (session, machine string, ok bool)

// Store is logs.db: one file, one writer, a read pool. Every method is safe
// for concurrent use; writes serialize on the writer goroutine.
type Store struct {
	db      *store.Store
	log     *slog.Logger
	now     func() time.Time
	resolve Resolver
	m       *metrics
}

// Options builds a [Store]. Resolve maps panes to sessions; nil means every
// pane is unknown until wired (ingest then answers UNKNOWN_STREAM).
type Options struct {
	Dir       string
	Telemetry *telemetry.Telemetry
	Resolve   Resolver
	Now       func() time.Time
}

// Open opens (creating) dir/logs.db with the row-25 migrations and starts
// its writer. MustExist semantics stay with statedb: logs.db is hub state
// the hub itself creates on first run. The file holds terminal output that
// can contain secrets, so it is created mode 0600 and an existing file
// with group or other access is refused, like the state folder's checks.
func Open(ctx context.Context, o Options) (*Store, error) {
	if o.Dir == "" {
		return nil, errors.New("logstore: Dir is required")
	}
	if err := ensureFile(filepath.Join(o.Dir, "logs.db")); err != nil {
		return nil, err
	}
	resolve := o.Resolve
	if resolve == nil {
		resolve = func(Pane) (string, string, bool) { return "", "", false }
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	m, err := newMetrics(o.Telemetry)
	if err != nil {
		return nil, err
	}
	log := slog.New(slog.DiscardHandler)
	if o.Telemetry != nil {
		log = o.Telemetry.Logger.With("component", "logstore")
	}
	db, err := store.Open(ctx, store.Options{
		Path:       filepath.Join(o.Dir, "logs.db"),
		Name:       "logs",
		Migrations: Migrations(),
		Telemetry:  o.Telemetry,
	})
	if err != nil {
		return nil, fmt.Errorf("logstore: open logs.db: %w", err)
	}
	return &Store{db: db, log: log, now: now, resolve: resolve, m: m}, nil
}

// Close stops the writer after it commits every queued job.
func (s *Store) Close() error { return s.db.Close() }

// SchemaVersion is the highest migration applied to the file.
func (s *Store) SchemaVersion() int { return s.db.SchemaVersion() }
