package logstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits from 02b and 03d W9.
const (
	maxQueryBytes   = 256
	maxSnippetBytes = 512
	// maxScanBytes bounds one call's decompressed scan; the caller loops on
	// the token for a wider search.
	maxScanBytes = 256 << 20
)

// Match is one search hit: the matching line with ANSI stripped.
type Match struct {
	Pane      Pane
	Stream    int
	RawOffset uint64
	Time      time.Time
	Snippet   string
}

// SearchQuery finds text across stored sessions.
type SearchQuery struct {
	// Query is the case-insensitive literal substring, 1 to 256 bytes.
	Query string
	// Machine, Goal, State, Since, Until select sessions; Limit caps
	// matches; PageToken continues.
	Filter    Filter
	Limit     int
	PageToken string
}

// Search scans the selected sessions' decoded frames for the query, at most
// 256 MiB of raw text per call. The token resumes where the scan stopped;
// empty means the whole selection was scanned.
func (s *Store) Search(ctx context.Context, q SearchQuery) ([]Match, string, uint64, error) {
	if len(q.Query) == 0 || len(q.Query) > maxQueryBytes {
		return nil, "", 0, fmt.Errorf("logstore: query is %d bytes, want 1..%d", len(q.Query), maxQueryBytes)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	needle := strings.ToLower(q.Query)
	resume, err := decodeSearchToken(q.PageToken)
	if err != nil {
		return nil, "", 0, err
	}
	sessions, err := s.searchSessions(ctx, q.Filter)
	if err != nil {
		return nil, "", 0, err
	}
	var matches []Match
	var scanned uint64
	var cursor searchCursor
	for _, id := range sessions {
		if id < resume.session {
			continue
		}
		stop, err := s.searchSession(ctx, id, needle, resume, limit, &matches, &scanned, &cursor)
		if err != nil {
			return nil, "", 0, err
		}
		if stop {
			return matches, cursor.token(), scanned, nil
		}
	}
	return matches, "", scanned, nil
}

// searchCursor is the resume point: the raw offset to continue from.
type searchCursor struct {
	session string
	pane    string
	stream  int
	offset  uint64
	set     bool
}

// token encodes the cursor with each field base64ed: pane strings carry
// the \x1f separator themselves, so a flat join would not split back.
func (c searchCursor) token() string {
	if !c.set {
		return ""
	}
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(c.session)) + "." + enc([]byte(c.pane)) + "." +
		enc([]byte(fmt.Sprintf("%d", c.stream))) + "." + enc([]byte(fmt.Sprintf("%d", c.offset)))
}

func decodeSearchToken(tok string) (searchCursor, error) {
	var c searchCursor
	if tok == "" {
		return c, nil
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 4 {
		return c, errors.New("logstore: bad search token")
	}
	dec := func(i int) (string, error) {
		raw, err := base64.RawURLEncoding.DecodeString(parts[i])
		if err != nil {
			return "", fmt.Errorf("logstore: bad search token: %w", err)
		}
		return string(raw), nil
	}
	var err error
	if c.session, err = dec(0); err != nil {
		return c, err
	}
	if c.pane, err = dec(1); err != nil {
		return c, err
	}
	stream, err := dec(2)
	if err != nil {
		return c, err
	}
	offset, err := dec(3)
	if err != nil {
		return c, err
	}
	var s, o uint64
	if _, err := fmt.Sscanf(stream, "%d", &s); err != nil {
		return c, fmt.Errorf("logstore: bad search token: %w", err)
	}
	if _, err := fmt.Sscanf(offset, "%d", &o); err != nil {
		return c, fmt.Errorf("logstore: bad search token: %w", err)
	}
	c.stream, c.offset, c.set = int(s), o, true
	return c, nil
}

// searchSessions lists candidate session ids in ascending order.
func (s *Store) searchSessions(ctx context.Context, f Filter) ([]string, error) {
	var conds []string
	var args []any
	if f.Machine != "" {
		conds = append(conds, "machine_id=?")
		args = append(args, f.Machine)
	}
	if f.State != "" {
		conds = append(conds, "state=?")
		args = append(args, f.State)
	}
	if !f.Since.IsZero() {
		conds = append(conds, "last_time>=?")
		args = append(args, f.Since.UTC().UnixNano())
	}
	if !f.Until.IsZero() {
		conds = append(conds, "last_time<=?")
		args = append(args, f.Until.UTC().UnixNano())
	}
	if f.Goal != "" {
		conds = append(conds, `goals LIKE ? ESCAPE '\'`)
		args = append(args, "%\""+likeEscape(f.Goal)+"\"%")
	}
	q := `SELECT session_id FROM log_sessions`
	if len(conds) > 0 {
		//nolint:gosec // G202: filter clauses are fixed literals; values are bound args
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY session_id ASC"
	var ids []string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("logstore: search sessions: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return fmt.Errorf("logstore: scan session: %w", err)
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// searchSession scans one session's streams in (pane, stream, offset)
// order. True means a budget ran out: matches holds the hits so far and
// the token resumes at cursor.
func (s *Store) searchSession(ctx context.Context, session, needle string, resume searchCursor,
	limit int, matches *[]Match, scanned *uint64, cursor *searchCursor,
) (bool, error) {
	type streamKey struct {
		pane   string
		stream int
	}
	var keys []streamKey
	rowsByStream := map[streamKey][]chunkRow{}
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT pane, stream, raw_offset, raw_length, frame_start,
			frame_raw_offset, last_time, data FROM log_chunks
			WHERE session_id=? ORDER BY pane, stream, raw_offset`, session)
		if err != nil {
			return fmt.Errorf("logstore: search rows: %w", err)
		}
		defer func() { _ = rows.Close() }()
		seen := map[streamKey]bool{}
		for rows.Next() {
			var pane string
			var stream int
			var r chunkRow
			var last sql.NullInt64
			var off, ln, foff int64
			var fs int
			if err := rows.Scan(&pane, &stream, &off, &ln, &fs, &foff, &last, &r.data); err != nil {
				return fmt.Errorf("logstore: scan chunk: %w", err)
			}
			// Raw offsets and lengths of our own stored chunks are never negative.
			r.offset, r.length = uint64(off), uint64(ln) //nolint:gosec // G115: stored offsets are never negative
			// Frame offsets of our own stored chunks are never negative.
			r.frameStart, r.frameOffset = fs == 1, uint64(foff) //nolint:gosec // G115: stored offsets are never negative
			if last.Valid {
				r.lastTime = time.Unix(0, last.Int64).UTC()
			}
			k := streamKey{pane: pane, stream: stream}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
			rowsByStream[k] = append(rowsByStream[k], r)
		}
		return rows.Err()
	})
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		if session < resume.session {
			break
		}
		if resume.set && session == resume.session &&
			(k.pane < resume.pane || (k.pane == resume.pane && k.stream < resume.stream)) {
			continue
		}
		done, err := scanStream(session, k.pane, k.stream, rowsByStream[k], needle,
			resume, limit, matches, scanned, cursor)
		if err != nil || done {
			return done, err
		}
	}
	return false, nil
}

// scanStream decodes one stream frame by frame and matches stripped lines.
// Frames ending at or before the resume offset are skipped without
// decoding. True stops the call: the scan or match budget ran out.
func scanStream(session, pane string, stream int, rows []chunkRow, needle string,
	resume searchCursor, limit int, matches *[]Match, scanned *uint64, cursor *searchCursor,
) (bool, error) {
	p, err := ParsePane(pane)
	if err != nil {
		return false, err
	}
	start := uint64(0)
	if resume.set && session == resume.session && pane == resume.pane && stream == resume.stream {
		start = resume.offset
	}
	// Group rows into frames: a frame_start row opens a new frame.
	type frame struct {
		offset uint64
		rows   []chunkRow
	}
	var frames []frame
	for _, r := range rows {
		if r.frameStart || len(frames) == 0 {
			frames = append(frames, frame{offset: r.frameOffset})
		}
		frames[len(frames)-1].rows = append(frames[len(frames)-1].rows, r)
	}
	for _, f := range frames {
		var end uint64
		for _, r := range f.rows {
			if e := r.offset + r.length; e > end {
				end = e
			}
		}
		if end <= start {
			continue
		}
		var raw [][]byte
		for _, r := range f.rows {
			raw = append(raw, r.data)
		}
		decoded, err := decodeFrame(bytes.Join(raw, nil))
		if err != nil {
			return false, fmt.Errorf("logstore: decode frame at %d: %w", f.offset, err)
		}
		*scanned += uint64(len(decoded))
		lineStart := f.offset
		keep := start > f.offset
		for _, line := range bytes.SplitAfter(decoded, []byte{'\n'}) {
			next := lineStart + uint64(len(line))
			if next > start {
				stripped := StripANSI(string(bytes.TrimSuffix(line, []byte{'\n'})))
				if strings.Contains(strings.ToLower(stripped), needle) {
					at := lineStart
					if keep && at < start {
						at = start
					}
					*matches = append(*matches, Match{
						Pane: p, Stream: stream,
						RawOffset: at, Time: rowTime(f.rows, at), Snippet: snippet(stripped),
					})
					if len(*matches) >= limit {
						*cursor = searchCursor{
							session: session, pane: pane,
							stream: stream, offset: next, set: true,
						}
						return true, nil
					}
				}
			}
			lineStart = next
		}
		if *scanned >= maxScanBytes {
			*cursor = searchCursor{session: session, pane: pane, stream: stream, offset: end, set: true}
			return true, nil
		}
	}
	return false, nil
}

// rowTime is the last_time of the chunk holding the raw offset.
func rowTime(rows []chunkRow, at uint64) time.Time {
	for _, r := range rows {
		if at >= r.offset && at < r.offset+r.length {
			return r.lastTime
		}
	}
	if len(rows) > 0 {
		return rows[len(rows)-1].lastTime
	}
	return time.Time{}
}

// snippet truncates a stripped line to 512 bytes on a rune boundary.
func snippet(s string) string {
	if len(s) <= maxSnippetBytes {
		return s
	}
	cut := 0
	for i := range s {
		if i > maxSnippetBytes {
			break
		}
		cut = i
	}
	if cut == 0 {
		cut = maxSnippetBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
	}
	return s[:cut]
}
