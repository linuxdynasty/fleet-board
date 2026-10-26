package spool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

// DefaultPoll is the spool rescan gap: hook events are per-turn, so a
// short poll reads them within a second without spinning.
const DefaultPoll = time.Second

// Tailer follows the spool file, running each new line through TurnEnd.
// Offset is the byte count already handled, kept in memory and in the
// sidecar file offsetPath (the spool path plus ".offset"), fsynced
// after every line so a restart replays at most one line. Nothing
// truncates the spool file: row 25 (shipping) owns rotation, so it
// grows with hook traffic until then.
type Tailer struct {
	spool  string
	offset string
	poll   time.Duration
	reg    Registry
	files  goalfile.Writer
	alive  func(uint32) bool
	off    int64
}

// Config wires a Tailer. Spool is hook-events.jsonl (or FLEET_HOOK_SPOOL);
// OffsetFile defaults to the spool path plus ".offset". Alive defaults
// to goalregistry.AlivePID.
type Config struct {
	Spool      string
	OffsetFile string
	Poll       time.Duration
	Registry   Registry
	Files      goalfile.Writer
	Alive      func(uint32) bool
}

// New checks the seams and loads the kept offset (missing files start
// at zero: the spool appears when the first hook fires).
func New(cfg Config) (*Tailer, error) {
	if cfg.Spool == "" {
		return nil, fmt.Errorf("spool: Spool is required")
	}
	if cfg.Registry == nil {
		return nil, fmt.Errorf("spool: Registry is required")
	}
	t := &Tailer{
		spool: cfg.Spool, offset: cfg.OffsetFile, poll: cfg.Poll,
		reg: cfg.Registry, files: cfg.Files, alive: cfg.Alive,
	}
	if t.offset == "" {
		t.offset = cfg.Spool + ".offset"
	}
	if t.poll <= 0 {
		t.poll = DefaultPoll
	}
	off, err := readOffset(t.offset)
	if err != nil {
		return nil, err
	}
	t.off = off
	return t, nil
}

// readOffset reads the kept byte count; a missing file starts at zero,
// and a corrupt one is an error (never a guess at zero: replaying the
// whole spool would duplicate turn-end events).
func readOffset(path string) (int64, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: the tailer's own offset file
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("spool: read offset: %w", err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("spool: corrupt offset %q", strings.TrimSpace(string(raw)))
	}
	return n, nil
}

// writeOffset fsyncs the byte count.
func writeOffset(path string, off int64) error {
	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("spool: mkdir offset: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: the tailer's own offset file
	if err != nil {
		return fmt.Errorf("spool: open offset: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprintf(f, "%d\n", off); err != nil {
		return fmt.Errorf("spool: write offset: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("spool: sync offset: %w", err)
	}
	return nil
}

// Run follows the spool until ctx ends. A missing spool waits for the
// first hook; a shrunk spool (rotated away) restarts at zero. One bad
// line never stops the tail: it is skipped and the offset moves past
// it, since the hook already failed open when it wrote the line.
func (t *Tailer) Run(ctx context.Context) error {
	for {
		if err := t.once(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(t.poll):
		}
	}
}

// once reads every line past the offset. The offset moves past each
// line only after HandleLine returns: silent skips move it too (the
// line is judged, not pending), while a TurnEnd failure keeps the line
// for the next pass.
func (t *Tailer) once(ctx context.Context) error {
	data, err := os.ReadFile(t.spool)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("spool: read spool: %w", err)
	}
	if int64(len(data)) < t.off {
		t.off = 0
	}
	rest := data[t.off:]
	for len(rest) > 0 {
		line := rest
		next := int64(len(data))
		if i := indexNewline(rest); i >= 0 {
			line = rest[:i]
			next = t.off + int64(i) + 1
		}
		if len(line) > 0 {
			if herr := HandleLine(ctx, line, t.reg, t.files, t.alive); herr != nil && !IsSkip(herr) {
				return herr
			}
		}
		t.off = next
		if err := writeOffset(t.offset, t.off); err != nil {
			return err
		}
		rest = data[t.off:]
	}
	return nil
}

func indexNewline(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}
