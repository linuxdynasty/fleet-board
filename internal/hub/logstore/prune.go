package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Stat is the store behind GetLogStoreStatus: live bytes against the cap,
// counts, the oldest data and the last prune.
type Stat struct {
	// UsedBytes is live pages times page size: freed pages not yet
	// vacuumed are not counted, so the file on disk can be larger.
	UsedBytes uint64
	FileBytes uint64
	Sessions  int
	Streams   int
	Oldest    time.Time
	HasOldest bool
	LastPrune time.Time
}

// Stat reads the live-byte measure (page_count minus freelist times page
// size), the session and stream counts, the oldest data and the last
// prune time. Retention keeps UsedBytes under the cap, not the file.
func (s *Store) Stat(ctx context.Context) (Stat, error) {
	var st Stat
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		st.UsedBytes, st.FileBytes, err = liveBytes(ctx, tx)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM log_sessions`).Scan(&st.Sessions); err != nil {
			return fmt.Errorf("logstore: count sessions: %w", err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
			SELECT DISTINCT session_id, pane, stream FROM log_chunks)`).Scan(&st.Streams); err != nil {
			return fmt.Errorf("logstore: count streams: %w", err)
		}
		var oldest sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MIN(first_time) FROM log_sessions
			WHERE first_time IS NOT NULL`).Scan(&oldest); err != nil {
			return fmt.Errorf("logstore: oldest time: %w", err)
		}
		if oldest.Valid {
			st.Oldest = time.Unix(0, oldest.Int64).UTC()
			st.HasOldest = true
		}
		var last sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT value FROM log_meta WHERE key='last_prune_time'`).Scan(&last); err != nil {
			if err != sql.ErrNoRows {
				return fmt.Errorf("logstore: last prune: %w", err)
			}
		}
		if last.Valid && last.Int64 > 0 {
			st.LastPrune = time.Unix(0, last.Int64).UTC()
		}
		return nil
	})
	return st, err
}

// liveBytes is (page_count - freelist_count) * page_size from PRAGMAs: the
// retention measure. Pages a prune freed but incremental_vacuum has not yet
// reclaimed do not count.
func liveBytes(ctx context.Context, tx *sql.Tx) (used, file uint64, err error) {
	var pages, free, size int64
	for _, q := range []struct {
		pragma string
		dest   *int64
	}{
		{"PRAGMA page_count", &pages},
		{"PRAGMA freelist_count", &free},
		{"PRAGMA page_size", &size},
	} {
		if err := tx.QueryRowContext(ctx, q.pragma).Scan(q.dest); err != nil {
			return 0, 0, fmt.Errorf("logstore: %s: %w", q.pragma, err)
		}
	}
	if pages < 0 || free < 0 || size <= 0 || free > pages {
		return 0, 0, fmt.Errorf("logstore: bad page counts pages=%d free=%d size=%d", pages, free, size)
	}
	return uint64(pages-free) * uint64(size), uint64(pages) * uint64(size), nil
}

// PruneResult is one retention pass.
type PruneResult struct {
	PrunedSessions int
	PrunedBytes    uint64
	// LiveBytes is the measure after the pass.
	LiveBytes uint64
	// Blocked names why the store stays over the cap: "no_final_session"
	// (only open or live sessions left) or "" when held or under the cap.
	Blocked string
}

// Reasons a prune stays blocked.
const BlockNoFinalSession = "no_final_session"

// vacuumStep is the incremental_vacuum page count per step, and
// maxVacuumSteps bounds one pass so a huge freelist cannot stall the
// writer: batches behind a prune wait at most one step.
const (
	vacuumStep     = 2000
	maxVacuumSteps = 64
)

// Prune deletes the oldest ended sessions until the live bytes are under
// capBytes (age-limited first when retention is set), then reclaims freed
// pages in 2,000-page vacuum steps. A session with an open stream is never
// a candidate. Each delete and each vacuum step is its own transaction, so
// incoming batches keep committing during a long prune. Output is never
// dropped for the cap: over the cap with no finished session left, the
// store keeps committing and reports Blocked.
func (s *Store) Prune(ctx context.Context, capBytes uint64, retention time.Duration) (PruneResult, error) {
	var out PruneResult
	now := s.now().UTC()
	if err := s.db.Write(ctx, func(tx *sql.Tx) error {
		return setMeta(ctx, tx, "last_prune_time", now.UnixNano())
	}); err != nil {
		return out, err
	}
	for {
		live, _, err := s.live(ctx)
		if err != nil {
			return out, err
		}
		out.LiveBytes = live
		candidate, age, err := s.nextCandidate(ctx, retention, now)
		if err != nil {
			return out, err
		}
		overCap := live > capBytes
		if candidate == "" || (!age && !overCap) {
			if overCap && candidate == "" {
				out.Blocked = BlockNoFinalSession
				s.log.Info("log.prune_blocked", "reason", BlockNoFinalSession,
					"live_bytes", live, "cap_bytes", capBytes)
			}
			break
		}
		freed, err := s.deleteSession(ctx, candidate)
		if err != nil {
			return out, err
		}
		out.PrunedSessions++
		out.PrunedBytes += freed
		s.m.pruned.Add(ctx, int64(freed)) //nolint:gosec // G115: pruned byte counts fit int64
		s.log.Info("log.pruned", "session", candidate, "bytes", freed)
	}
	if err := s.vacuum(ctx); err != nil {
		return out, err
	}
	live, _, err := s.live(ctx)
	if err != nil {
		return out, err
	}
	out.LiveBytes = live
	if live > capBytes && out.Blocked == "" {
		out.Blocked = BlockNoFinalSession
		s.log.Info("log.prune_blocked", "reason", BlockNoFinalSession,
			"live_bytes", live, "cap_bytes", capBytes)
	}
	return out, nil
}

// nextCandidate is the oldest ended session with no open stream: an
// age-expired one first, then the oldest end_time. Empty means none is
// deletable; age says the candidate expired by retention rather than the
// cap. last_time orders sessions that ended without an end_time.
func (s *Store) nextCandidate(ctx context.Context, retention time.Duration, now time.Time) (string, bool, error) {
	var id string
	var age bool
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		if retention > 0 {
			cutoff := now.Add(-retention).UnixNano()
			found, err := oldestClosed(ctx, tx, `SELECT session_id FROM log_sessions
				WHERE state='ENDED' AND end_time IS NOT NULL AND end_time < ?
				ORDER BY end_time ASC LIMIT 20`, cutoff)
			if err != nil {
				return err
			}
			if found != "" {
				id, age = found, true
				return nil
			}
		}
		found, err := oldestClosed(ctx, tx, `SELECT session_id FROM log_sessions
			WHERE state='ENDED' ORDER BY COALESCE(end_time, last_time, 0) ASC LIMIT 20`)
		if err != nil {
			return err
		}
		id = found
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return id, age, nil
}

// oldestClosed is the first candidate of the query with no open stream.
func oldestClosed(ctx context.Context, tx *sql.Tx, q string, args ...any) (string, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return "", fmt.Errorf("logstore: prune candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return "", fmt.Errorf("logstore: scan candidate: %w", err)
		}
		open, err := openStreams(ctx, tx, c)
		if err != nil {
			return "", err
		}
		if !open {
			return c, nil
		}
	}
	return "", rows.Err()
}

// deleteSession removes one session in one transaction, returning the freed
// stored bytes.
func (s *Store) deleteSession(ctx context.Context, session string) (uint64, error) {
	var freed uint64
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(LENGTH(data)),0) FROM log_chunks
			WHERE session_id=?`, session).Scan(&freed); err != nil {
			return fmt.Errorf("logstore: freed bytes: %w", err)
		}
		for _, q := range []string{
			`DELETE FROM log_chunks WHERE session_id=?`,
			`DELETE FROM archived_pane_summaries WHERE session_id=?`,
			`DELETE FROM log_sessions WHERE session_id=?`,
		} {
			if _, err := tx.ExecContext(ctx, q, session); err != nil {
				return fmt.Errorf("logstore: prune delete: %w", err)
			}
		}
		return nil
	})
	return freed, err
}

// vacuum reclaims freed pages in bounded steps, one transaction per step.
func (s *Store) vacuum(ctx context.Context) error {
	for i := 0; i < maxVacuumSteps; i++ {
		var free int64
		err := s.db.Read(ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&free)
		})
		if err != nil {
			return err
		}
		if free <= 0 {
			return nil
		}
		if err := s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, vacuumStep))
			return err
		}); err != nil {
			return fmt.Errorf("logstore: vacuum: %w", err)
		}
	}
	return nil
}

// live reads the current live and file bytes.
func (s *Store) live(ctx context.Context) (uint64, uint64, error) {
	var used, file uint64
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		used, file, err = liveBytes(ctx, tx)
		return err
	})
	return used, file, err
}

func setMeta(ctx context.Context, tx *sql.Tx, key string, value int64) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO log_meta(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
