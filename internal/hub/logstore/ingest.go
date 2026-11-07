package logstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// maxChunkBytes caps one stored chunk's compressed data: the 1 MiB message
// cap leaves room for the envelope (02b, 03d W9).
const maxChunkBytes = 960 * 1024

// Ingest stores one chunk and answers with the stream's new end and the
// verdict LogAck carries. It resolves the session from the chunk, or from
// the pane when the agent left it empty (a developer's own session). The
// ack follows the commit: Write returns only after it.
//
// Accept rules (03d W9), per stream, where end is the stored end:
//   - offset + raw_length <= end: DUPLICATE, nothing stored, end re-acked.
//   - offset == end + gap_before: STORED, the row records gap_before.
//   - anything else: HOLE with the hub's end, nothing stored.
//   - the pane maps to no session: UNKNOWN_STREAM, nothing stored.
//
// Ingest files one chunk and reports where the stream ends. On a non-nil
// error the verdict and end carry no meaning: the caller must not act on
// them (invalid input returns Hole with a zero end only as a placeholder).
func (s *Store) Ingest(ctx context.Context, c Chunk) (end uint64, v Verdict, err error) {
	session, machine := c.Session, c.Machine
	if session == "" {
		var ok bool
		session, machine, ok = s.resolve(c.Pane)
		if !ok {
			s.m.chunks.Add(ctx, 1, verAttr(UnknownStream))
			return 0, UnknownStream, nil
		}
	}
	if machine == "" {
		return 0, Hole, fmt.Errorf("logstore: machine is required for session %q", session)
	}
	if len(c.Data) > maxChunkBytes {
		return 0, Hole, fmt.Errorf("logstore: chunk data %d bytes is above %d", len(c.Data), maxChunkBytes)
	}
	if c.Stream != TerminalStream && c.Stream != TranscriptStream {
		return 0, Hole, fmt.Errorf("logstore: stream %d is not TERMINAL or HARNESS_TRANSCRIPT", c.Stream)
	}
	pane := c.Pane.String()
	first, last := unixNanos(c.FirstTime), unixNanos(c.LastTime)
	// The batch measure is enqueue-to-commit for the caller's chunk: the
	// store's own batching may hold the job up to its window first.
	batchStart := s.now()
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO log_sessions(session_id, machine_id, first_time, last_time)
			VALUES(?, ?, ?, ?)
			ON CONFLICT(session_id) DO NOTHING`,
			session, machine, first, last); err != nil {
			return fmt.Errorf("logstore: ensure session: %w", err)
		}
		end, err = streamEnd(ctx, tx, session, pane, c.Stream)
		if err != nil {
			return err
		}
		if c.Offset+c.UintRaw() <= end {
			v = Duplicate
			if c.Final && c.Offset+c.UintRaw() == end {
				// The pane-exit final marker arrived after its bytes:
				// record it on the stream's newest chunk so the stream
				// reads closed. A stale Final short of the end is not
				// the stream's end and is ignored.
				if _, err := tx.ExecContext(ctx, `UPDATE log_chunks SET is_final=1
					WHERE session_id=? AND pane=? AND stream=?
					AND raw_offset+raw_length=(SELECT MAX(raw_offset+raw_length)
					FROM log_chunks WHERE session_id=? AND pane=? AND stream=?)`,
					session, pane, c.Stream, session, pane, c.Stream); err != nil {
					return fmt.Errorf("logstore: mark final: %w", err)
				}
			}
			return nil
		}
		if c.Offset != end+c.GapBefore {
			v = Hole
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO log_chunks(session_id, pane, stream, raw_offset, raw_length,
				frame_start, frame_raw_offset, first_time, last_time, gap_before, is_final, goal_id, data)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session, pane, c.Stream, c.Offset, c.RawLength,
			boolInt(c.FrameStart), c.FrameRawOffset, first, last, c.GapBefore, boolInt(c.Final), c.Goal, c.Data,
		); err != nil {
			return fmt.Errorf("logstore: insert chunk: %w", err)
		}
		end = c.Offset + c.UintRaw()
		v = Stored
		if _, err := tx.ExecContext(ctx, `UPDATE log_sessions SET last_time=?,
			raw_bytes=raw_bytes+?, stored_bytes=stored_bytes+?, gap_bytes=gap_bytes+?
			WHERE session_id=?`, last, c.UintRaw()+c.GapBefore, len(c.Data), c.GapBefore, session); err != nil {
			return fmt.Errorf("logstore: update session totals: %w", err)
		}
		if c.Goal != "" {
			if err := addGoal(ctx, tx, session, c.Goal); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, Hole, err
	}
	s.m.chunks.Add(ctx, 1, verAttr(v))
	s.m.batches.Record(ctx, s.now().Sub(batchStart).Seconds())
	switch v {
	case Hole:
		s.log.Info("log.hole_refused", "session", session, "offset", c.Offset, "end", end)
	case Stored:
		if c.GapBefore > 0 {
			s.log.Info("log.gap", "session", session, "machine", machine, "bytes_lost", c.GapBefore)
		}
	case Duplicate, UnknownStream:
		// No log line: duplicates and unknown streams are routine.
	}
	return end, v, nil
}

// streamEnd is the stored end of one stream: the furthest raw byte held.
func streamEnd(ctx context.Context, tx *sql.Tx, session, pane string, stream int) (uint64, error) {
	var end sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(raw_offset + raw_length) FROM log_chunks
		WHERE session_id=? AND pane=? AND stream=?`, session, pane, stream).Scan(&end); err != nil {
		return 0, fmt.Errorf("logstore: stream end: %w", err)
	}
	if !end.Valid {
		return 0, nil
	}
	return uint64(end.Int64), nil //nolint:gosec // G115: raw offsets are never negative
}

// addGoal records a goal on the session once; the goals column is a JSON
// array the List read parses.
func addGoal(ctx context.Context, tx *sql.Tx, session, goal string) error {
	var goals string
	if err := tx.QueryRowContext(ctx, `SELECT goals FROM log_sessions WHERE session_id=?`, session).Scan(&goals); err != nil {
		return fmt.Errorf("logstore: read goals: %w", err)
	}
	list := goalsOf(goals)
	for _, g := range list {
		if g == goal {
			return nil
		}
	}
	raw, err := json.Marshal(append(list, goal))
	if err != nil {
		return fmt.Errorf("logstore: encode goals: %w", err)
	}
	goals = string(raw)
	if _, err := tx.ExecContext(ctx, `UPDATE log_sessions SET goals=? WHERE session_id=?`, goals, session); err != nil {
		return fmt.Errorf("logstore: add goal: %w", err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// goalsOf reads the goals column: a JSON array of goal ids.
func goalsOf(s string) []string {
	var out []string
	if s == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func unixNanos(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().UnixNano()
}

// UintRaw widens the raw length for offset arithmetic.
func (c Chunk) UintRaw() uint64 { return uint64(c.RawLength) }
