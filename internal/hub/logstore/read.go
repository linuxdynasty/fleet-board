package logstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Span is one contiguous run of a stream's raw bytes. A gap (bytes the
// agent's spool cap dropped) starts a new span carrying GapBefore.
type Span struct {
	Offset    uint64
	Data      []byte
	GapBefore uint64
	FirstTime time.Time
	LastTime  time.Time
	// Final marks the span holding the stream's end when the stream ended.
	Final bool
}

// chunkRow is one log_chunks row.
type chunkRow struct {
	offset      uint64
	length      uint64
	frameStart  bool
	frameOffset uint64
	firstTime   time.Time
	lastTime    time.Time
	gapBefore   uint64
	isFinal     bool
	data        []byte
}

// Read replays one stream from start, stopping after maxBytes raw bytes
// (0 means to the end). It decodes from the frame start the offset falls
// in, so any offset replays byte-exact. Gaps split spans.
func (s *Store) Read(ctx context.Context, session string, pane Pane, stream int, start, maxBytes uint64) ([]Span, error) {
	var rows []chunkRow
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		rows, err = streamRows(ctx, tx, session, pane.String(), stream)
		return err
	})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	// Index rows by frame start for one decode per frame. A frame
	// missing its head (cap eviction, crash) or torn cannot decode:
	// leaving it out of the map skips its rows below and marks the gap
	// at the next span, instead of failing the whole read.
	frames := map[uint64][]chunkRow{}
	for _, r := range rows {
		frames[r.frameOffset] = append(frames[r.frameOffset], r)
	}
	// Frames decode lazily on first touch inside the window: a windowed
	// read must not pay for frames it never serves.
	decoded := map[uint64][]byte{}
	failed := map[uint64]bool{}
	decode := func(f uint64) ([]byte, bool) {
		if b, ok := decoded[f]; ok {
			return b, true
		}
		if failed[f] {
			return nil, false
		}
		var raw [][]byte
		for _, r := range frames[f] {
			raw = append(raw, r.data)
		}
		b, err := decodeFrame(bytes.Join(raw, nil))
		if err != nil {
			failed[f] = true
			return nil, false
		}
		decoded[f] = b
		return b, true
	}
	var spans []Span
	var cur *Span
	var lastEnd uint64
	haveLast := false
	skipped := false
	end := ^uint64(0)
	if maxBytes > 0 {
		end = start + maxBytes
	}
	flush := func() {
		if cur != nil && len(cur.Data) > 0 {
			spans = append(spans, *cur)
			cur = nil
		}
	}
	for _, r := range rows {
		rowEnd := r.offset + r.length
		if rowEnd <= start || r.offset >= end {
			continue
		}
		b, ok := decode(r.frameOffset)
		if !ok {
			flush()
			skipped = true
			continue
		}
		from := start
		if r.offset > from {
			from = r.offset
		}
		to := rowEnd
		if to > end {
			to = end
		}
		lo, hi := from-r.frameOffset, to-r.frameOffset
		if hi > uint64(len(b)) {
			return nil, fmt.Errorf("logstore: frame at %d holds %d bytes, need %d", r.frameOffset, len(b), hi)
		}
		if r.gapBefore > 0 && r.offset > start && cur != nil {
			flush()
		}
		if cur == nil {
			cur = &Span{Offset: from, FirstTime: r.firstTime}
			switch {
			case skipped && haveLast && r.offset > start:
				// Bytes vanished between the last emitted byte and
				// this row: the skipped frame plus any stored gap.
				cur.Offset = r.offset
				cur.GapBefore = r.offset - lastEnd
			case skipped && !haveLast && r.offset > start:
				cur.Offset = r.offset
				cur.GapBefore = r.offset - start
			case r.gapBefore > 0 && r.offset >= start:
				cur.Offset = r.offset
				cur.GapBefore = r.gapBefore
			}
		}
		skipped = false
		cur.Data = append(cur.Data, b[lo:hi]...)
		cur.LastTime = r.lastTime
		lastEnd, haveLast = to, true
		if r.isFinal && to == rowEnd && (maxBytes == 0 || rowEnd <= end) {
			cur.Final = true
		}
	}
	flush()
	if len(spans) == 0 {
		return nil, nil
	}
	return spans, nil
}

// streamRows loads one stream's chunks in offset order.
func streamRows(ctx context.Context, tx *sql.Tx, session, pane string, stream int) ([]chunkRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT raw_offset, raw_length, frame_start, frame_raw_offset,
		first_time, last_time, gap_before, is_final, data FROM log_chunks
		WHERE session_id=? AND pane=? AND stream=? ORDER BY raw_offset`, session, pane, stream)
	if err != nil {
		return nil, fmt.Errorf("logstore: read chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []chunkRow
	for rows.Next() {
		var r chunkRow
		var first, last sql.NullInt64
		var fs, fin int
		var data []byte
		var off, ln, foff, gap int64
		if err := rows.Scan(&off, &ln, &fs, &foff, &first, &last, &gap, &fin, &data); err != nil {
			return nil, fmt.Errorf("logstore: scan chunk: %w", err)
		}
		// Raw offsets and lengths of our own stored chunks are never negative.
		r = chunkRow{
			offset: uint64(off), length: uint64(ln), frameStart: fs == 1, //nolint:gosec // G115: stored offsets are never negative
			frameOffset: uint64(foff), gapBefore: uint64(gap), isFinal: fin == 1, data: data, //nolint:gosec // G115: stored offsets are never negative
		}
		if first.Valid {
			r.firstTime = time.Unix(0, first.Int64).UTC()
		}
		if last.Valid {
			r.lastTime = time.Unix(0, last.Int64).UTC()
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// decodeFrame decompresses one frame's blocks as received. An unterminated
// frame (the agent crashed mid-frame) still yields its whole blocks.
func decodeFrame(data []byte) ([]byte, error) {
	r, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		// An unterminated frame (the agent crashed mid-frame) still
		// yields its whole blocks; anything else is corruption.
		if errors.Is(err, io.ErrUnexpectedEOF) && len(out) > 0 {
			return out, nil
		}
		return nil, err
	}
	return out, nil
}

// encodeToken is the List resume point: the last row's time and session,
// each base64ed so session ids never collide with the separator.
func encodeToken(lastTime time.Time, session string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(lastTime.UTC().Format(time.RFC3339Nano))) + "." + enc([]byte(session))
}

// decodeToken inverts encodeToken.
func decodeToken(tok string) (time.Time, string, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("logstore: bad page token")
	}
	rawTime, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("logstore: bad page token: %w", err)
	}
	rawSession, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("logstore: bad page token: %w", err)
	}
	nanos, err := time.Parse(time.RFC3339Nano, string(rawTime))
	if err != nil {
		return time.Time{}, "", fmt.Errorf("logstore: bad page token: %w", err)
	}
	return nanos, string(rawSession), nil
}
