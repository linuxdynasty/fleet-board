package logstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// EndSession marks a session ended and archives its last pane summaries,
// which move from state.db to logs.db (ADR 0015). Unknown sessions are
// created: a session can end before any chunk arrives.
func (s *Store) EndSession(ctx context.Context, session, machine string, end time.Time, sums []Summary) error {
	if session == "" {
		return errors.New("logstore: session is required")
	}
	if machine == "" {
		return errors.New("logstore: machine is required")
	}
	now := s.now().UTC()
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO log_sessions(session_id, machine_id, state, end_time)
			VALUES(?, ?, 'ENDED', ?)
			ON CONFLICT(session_id) DO UPDATE SET state='ENDED', end_time=?`,
			session, machine, end.UTC().UnixNano(), end.UTC().UnixNano()); err != nil {
			return fmt.Errorf("logstore: end session: %w", err)
		}
		for _, su := range sums {
			if len(su.LastLines) > 4*1024 {
				return fmt.Errorf("logstore: summary for pane %q is above 4 KiB", su.Pane)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO archived_pane_summaries(session_id, pane, last_lines,
				last_output_time, activity, archived_time) VALUES(?, ?, ?, ?, ?, ?)
				ON CONFLICT(session_id, pane) DO UPDATE SET last_lines=excluded.last_lines,
				last_output_time=excluded.last_output_time, activity=excluded.activity,
				archived_time=excluded.archived_time`,
				session, su.Pane.String(), su.LastLines, unixNanos(su.LastOutputTime),
				su.Activity, now.UnixNano()); err != nil {
				return fmt.Errorf("logstore: archive summary: %w", err)
			}
		}
		return nil
	})
	return err
}

// openStreams reports whether any of the session's streams lacks a final
// chunk; the caller holds the tx.
func openStreams(ctx context.Context, tx *sql.Tx, session string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT pane, stream, MAX(raw_offset + raw_length) AS end FROM log_chunks
		WHERE session_id=? GROUP BY pane, stream) AS newest
		WHERE NOT EXISTS (SELECT 1 FROM log_chunks c
		WHERE c.session_id=? AND c.pane=newest.pane AND c.stream=newest.stream
		AND c.is_final=1 AND c.raw_offset + c.raw_length = newest.end)`,
		session, session).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("logstore: open streams: %w", err)
	}
	return n > 0, nil
}

// Delete removes one session's chunks, summaries and row in one
// transaction, returning the freed stored bytes. A session with a stream
// still capturing is refused ([ErrStreamOpen]): end capture first. The
// caller audits logs.delete with the byte count in state.db.
func (s *Store) Delete(ctx context.Context, session string) (uint64, error) {
	var freed uint64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_sessions WHERE session_id=?`, session).Scan(&n); err != nil {
			return fmt.Errorf("logstore: find session: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		open, err := openStreams(ctx, tx, session)
		if err != nil {
			return err
		}
		if open {
			return ErrStreamOpen
		}
		var stored sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT SUM(LENGTH(data)) FROM log_chunks WHERE session_id=?`,
			session).Scan(&stored); err != nil {
			return fmt.Errorf("logstore: freed bytes: %w", err)
		}
		if stored.Valid {
			// SUM over zero or more rows is never negative.
			freed = uint64(stored.Int64) //nolint:gosec // G115: SUM of lengths is never negative
		}
		for _, q := range []string{
			`DELETE FROM log_chunks WHERE session_id=?`,
			`DELETE FROM archived_pane_summaries WHERE session_id=?`,
			`DELETE FROM log_sessions WHERE session_id=?`,
		} {
			if _, err := tx.ExecContext(ctx, q, session); err != nil {
				return fmt.Errorf("logstore: delete session: %w", err)
			}
		}
		return nil
	})
	return freed, err
}
