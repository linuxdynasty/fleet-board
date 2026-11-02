package logship

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Stream identities, matching fleet.type.v1 LogStream values.
const (
	TerminalStream   = 1
	TranscriptStream = 2
)

// chunkExt is the spool file suffix; tmpExt marks an upload in progress.
const (
	chunkExt = ".chk"
	tmpExt   = ".tmp"
	ackedExt = ".acked"
)

// StoredChunk is one spooled chunk: the chunker's output plus the stream
// end marker and goal.
type StoredChunk struct {
	Offset         uint64
	Data           []byte
	RawLength      uint32
	FrameStart     bool
	FrameRawOffset uint64
	FirstTime      int64
	LastTime       int64
	Final          bool
	Goal           string
}

// chunkFile is the on-disk form: one JSON header line, then the zstd data.
type chunkFile struct {
	Offset         uint64 `json:"offset"`
	RawLength      uint32 `json:"raw_length"`
	FrameStart     bool   `json:"frame_start"`
	FrameRawOffset uint64 `json:"frame_raw_offset"`
	FirstTime      int64  `json:"first_time"`
	LastTime       int64  `json:"last_time"`
	Final          bool   `json:"final"`
	Goal           string `json:"goal,omitempty"`
}

// Spool is one stream's durable outbox (03d W9, K2): compressed chunks on
// disk plus the acked offset. Chunks stay until the hub acks them; the cap
// drops the oldest and the floor advances, so the next chunk carries the
// gap. One goroutine owns a Spool; Pause is the only shared flag.
type Spool struct {
	dir      string
	capBytes int64

	mu     sync.Mutex
	paused bool
}

// OpenSpool opens (creating) the spool for one stream. root is the agent
// state directory; the layout is spool/<server>/<start>/<pane>/<stream>/.
// Pane ids are %N only, server names are charset-checked, and start must be
// a single path element, so no hub or tmux text reaches the filesystem
// outside the spool directory.
func OpenSpool(root, server, start, pane string, stream int, capBytes int64) (*Spool, error) {
	if !serverRE.MatchString(server) {
		return nil, fmt.Errorf("logship: server %q is not a server name", server)
	}
	if start == "" || start == "." || start == ".." || filepath.Base(start) != start {
		return nil, fmt.Errorf("logship: start %q is not a single path element", start)
	}
	if !paneRE.MatchString(pane) {
		return nil, fmt.Errorf("logship: pane %q is not a pane id", pane)
	}
	if stream != TerminalStream && stream != TranscriptStream {
		return nil, fmt.Errorf("logship: stream %d is not TERMINAL or HARNESS_TRANSCRIPT", stream)
	}
	if capBytes <= 0 {
		return nil, fmt.Errorf("logship: cap %d bytes is not positive", capBytes)
	}
	dir := filepath.Join(root, "spool", server, start, pane[1:], fmt.Sprint(stream))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("logship: make spool dir: %w", err)
	}
	sp := &Spool{dir: dir, capBytes: capBytes}
	// A crash mid-chunk leaves a .tmp behind: it was never renamed, so it
	// was never a chunk.
	for _, e := range readdir(dir) {
		if len(e) > len(tmpExt) && e[len(e)-len(tmpExt):] == tmpExt {
			_ = os.Remove(filepath.Join(dir, e))
		}
	}
	return sp, nil
}

// Dir is the stream's spool directory, for tests and the shipper.
func (s *Spool) Dir() string { return s.dir }

// Pause stops shipping; capture continues into the spool. Resume clears it.
func (s *Spool) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = true
}

// Resume restarts shipping after a pause.
func (s *Spool) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = false
}

// Paused reports the shipping pause.
func (s *Spool) Paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// Append durables one chunk (tmp write, fsync, rename, dir sync) and
// enforces the cap oldest-first. It returns the raw bytes dropped by the
// cap, which the shipper counts as the stream's gap.
func (s *Spool) Append(c StoredChunk) (uint64, error) {
	head, err := json.Marshal(chunkFile{
		Offset: c.Offset, RawLength: c.RawLength, FrameStart: c.FrameStart,
		FrameRawOffset: c.FrameRawOffset, FirstTime: c.FirstTime, LastTime: c.LastTime,
		Final: c.Final, Goal: c.Goal,
	})
	if err != nil {
		return 0, fmt.Errorf("logship: encode chunk header: %w", err)
	}
	head = append(head, '\n')
	name := fmt.Sprintf("%020d%s", c.Offset, chunkExt)
	// The name is Sprintf of the chunk offset inside the spool's own dir.
	tmp, err := os.OpenFile(filepath.Join(s.dir, name+tmpExt), //nolint:gosec // G304: name is the chunk offset in the spool dir
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, fmt.Errorf("logship: write chunk: %w", err)
	}
	if _, err := tmp.Write(append(head, c.Data...)); err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("logship: write chunk: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("logship: sync chunk: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("logship: write chunk: %w", err)
	}
	if err := os.Rename(filepath.Join(s.dir, name+tmpExt), filepath.Join(s.dir, name)); err != nil {
		return 0, fmt.Errorf("logship: commit chunk: %w", err)
	}
	if err := syncDir(s.dir); err != nil {
		return 0, err
	}
	return s.enforceCap()
}

// Pending lists chunks past the acked offset, oldest first.
func (s *Spool) Pending() ([]StoredChunk, error) {
	acked, err := s.acked()
	if err != nil {
		return nil, err
	}
	var out []StoredChunk
	for _, name := range readdir(s.dir) {
		if len(name) <= len(chunkExt) || name[len(name)-len(chunkExt):] != chunkExt {
			continue
		}
		c, err := readChunk(filepath.Join(s.dir, name))
		if err != nil {
			return nil, err
		}
		// Acks land on chunk ends, so a chunk is either fully acked or
		// fully pending; a mid-chunk ack keeps the chunk and the hub
		// re-acks its end.
		if c.Offset <= acked && c.UintRaw() <= acked-c.Offset {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Offset < out[j].Offset })
	return out, nil
}

// UintRaw widens the raw length for offset arithmetic.
func (c StoredChunk) UintRaw() uint64 { return uint64(c.RawLength) }

// SetAcked durables the hub's new end and drops chunks under it. Chunks
// stay until acked, so the hub being away only grows the spool.
func (s *Spool) SetAcked(end uint64) error {
	for _, name := range readdir(s.dir) {
		if len(name) <= len(chunkExt) || name[len(name)-len(chunkExt):] != chunkExt {
			continue
		}
		c, err := readChunk(filepath.Join(s.dir, name))
		if err != nil {
			return err
		}
		if c.Offset+c.UintRaw() <= end {
			if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("logship: drop acked chunk: %w", err)
			}
		}
	}
	cur, err := s.acked()
	if err != nil {
		return err
	}
	if end <= cur {
		return nil
	}
	raw := make([]byte, 8)
	binary.LittleEndian.PutUint64(raw, end)
	if err := s.writeFileSync("acked"+ackedExt, raw); err != nil {
		return err
	}
	return nil
}

// writeFileSync durables one small file the way Append durables a chunk:
// tmp write, fsync, rename, dir sync. A crash leaves a .tmp behind (cleaned
// on open, so it can never become the file) and never a torn file.
func (s *Spool) writeFileSync(name string, data []byte) error {
	tmp, err := os.OpenFile(filepath.Join(s.dir, name+tmpExt), //nolint:gosec // G304: name is the fixed acked file in the spool dir
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("logship: write %s: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("logship: write %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("logship: sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("logship: write %s: %w", name, err)
	}
	if err := os.Rename(filepath.Join(s.dir, name+tmpExt), filepath.Join(s.dir, name)); err != nil {
		return fmt.Errorf("logship: commit %s: %w", name, err)
	}
	return syncDir(s.dir)
}

// Floor is the oldest retained raw offset: the next resend after a HOLE
// starts here with gap_before set when the hub's end is lower.
func (s *Spool) Floor() (uint64, error) {
	acked, err := s.acked()
	if err != nil {
		return 0, err
	}
	// The floor is the oldest retained chunk; with no chunks it is the
	// acked end. (An acked of 0 must not drag the floor below the files.)
	floor := acked
	found := false
	for _, name := range readdir(s.dir) {
		if len(name) <= len(chunkExt) || name[len(name)-len(chunkExt):] != chunkExt {
			continue
		}
		var off uint64
		if _, err := fmt.Sscanf(name[:len(name)-len(chunkExt)], "%d", &off); err != nil {
			continue
		}
		if !found || off < floor {
			floor, found = off, true
		}
	}
	return floor, nil
}

// acked reads the durable acked offset, 0 when unset.
func (s *Spool) acked() (uint64, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, "acked"+ackedExt))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("logship: read acked: %w", err)
	}
	if len(raw) != 8 {
		return 0, fmt.Errorf("logship: acked file holds %d bytes, want 8", len(raw))
	}
	return binary.LittleEndian.Uint64(raw), nil
}

// enforceCap drops the oldest chunks while the spool is over the cap,
// newest kept. It returns the dropped raw bytes.
func (s *Spool) enforceCap() (uint64, error) {
	var dropped uint64
	for {
		names := readdir(s.dir)
		var total int64
		oldest := ""
		var oldestOff uint64
		first := true
		for _, name := range names {
			if len(name) <= len(chunkExt) || name[len(name)-len(chunkExt):] != chunkExt {
				continue
			}
			fi, err := os.Stat(filepath.Join(s.dir, name))
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return dropped, fmt.Errorf("logship: stat chunk: %w", err)
			}
			total += fi.Size()
			var off uint64
			if _, err := fmt.Sscanf(name[:len(name)-len(chunkExt)], "%d", &off); err != nil {
				continue
			}
			if first || off < oldestOff {
				oldest, oldestOff, first = name, off, false
			}
		}
		if total <= s.capBytes || oldest == "" {
			return dropped, nil
		}
		// Never drop the last chunk: the stream's newest bytes stay even
		// over the cap, and the gap counts what went before them.
		left := 0
		for _, name := range names {
			if len(name) > len(chunkExt) && name[len(name)-len(chunkExt):] == chunkExt {
				left++
			}
		}
		if left <= 1 {
			return dropped, nil
		}
		c, err := readChunk(filepath.Join(s.dir, oldest))
		if err != nil {
			return dropped, err
		}
		if err := os.Remove(filepath.Join(s.dir, oldest)); err != nil && !os.IsNotExist(err) {
			return dropped, fmt.Errorf("logship: evict chunk: %w", err)
		}
		dropped += c.UintRaw()
	}
}

// readChunk reads one chunk file: the header line, then the zstd data.
func readChunk(path string) (StoredChunk, error) {
	// Paths come from the spool's own directory listing, never the hub.
	raw, err := os.ReadFile(path) //nolint:gosec // G304: path is a file in the spool's own dir
	if err != nil {
		return StoredChunk{}, fmt.Errorf("logship: read chunk: %w", err)
	}
	i := bytes.IndexByte(raw, '\n')
	if i < 0 {
		return StoredChunk{}, fmt.Errorf("logship: chunk %q has no header", path)
	}
	var h chunkFile
	if err := json.Unmarshal(raw[:i], &h); err != nil {
		return StoredChunk{}, fmt.Errorf("logship: chunk header: %w", err)
	}
	return StoredChunk{
		Offset: h.Offset, Data: raw[i+1:], RawLength: h.RawLength,
		FrameStart: h.FrameStart, FrameRawOffset: h.FrameRawOffset,
		FirstTime: h.FirstTime, LastTime: h.LastTime, Final: h.Final, Goal: h.Goal,
	}, nil
}

// readdir lists one directory's file names, sorted.
func readdir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// syncDir fsyncs the directory so a rename durables with it.
func syncDir(dir string) error {
	// The dir is the stream's own spool directory.
	d, err := os.Open(dir) //nolint:gosec // G304: dir is the stream's own spool directory
	if err != nil {
		return fmt.Errorf("logship: open spool dir: %w", err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("logship: sync spool dir: %w", err)
	}
	return nil
}
