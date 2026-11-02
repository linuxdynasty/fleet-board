package logstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound means no logs are stored for the session.
var ErrNotFound = errors.New("logstore: session not found")

// ErrStreamOpen means a Delete hit a session with a stream still capturing.
var ErrStreamOpen = errors.New("logstore: a stream is still capturing")

// Summary is one archived last pane summary.
type Summary struct {
	Pane           Pane
	LastLines      string
	LastOutputTime time.Time
	Activity       int
	ArchivedTime   time.Time
}

// StreamInfo describes one stored log stream of a session.
type StreamInfo struct {
	Pane        Pane
	Stream      int
	RawBytes    uint64
	StoredBytes uint64
	GapBytes    uint64
	FirstTime   time.Time
	LastTime    time.Time
	// Open means the stream has no final chunk: capture may resume.
	Open bool
}

// StoredSession is everything the hub keeps for one session's logs.
type StoredSession struct {
	ID             string
	Machine        string
	Name           string
	StartedByFleet bool
	State          string
	Goals          []string
	Harnesses      []string
	FirstTime      time.Time
	LastTime       time.Time
	EndTime        time.Time
	Ended          bool
	RawBytes       uint64
	StoredBytes    uint64
	GapBytes       uint64
	Streams        []StreamInfo
	Summaries      []Summary
}

// scanStored reads one log_sessions row; the caller holds the tx.
func scanStored(row *sql.Row) (StoredSession, error) {
	var st StoredSession
	var state, goals, harnesses string
	var first, last, end sql.NullInt64
	var fleet int
	err := row.Scan(&st.ID, &st.Machine, &st.Name, &fleet, &state, &goals, &harnesses,
		&first, &last, &end, &st.RawBytes, &st.StoredBytes, &st.GapBytes)
	if err != nil {
		return StoredSession{}, err
	}
	st.StartedByFleet = fleet == 1
	st.State = state
	st.Goals = goalsOf(goals)
	st.Harnesses = goalsOf(harnesses)
	if first.Valid {
		st.FirstTime = time.Unix(0, first.Int64).UTC()
	}
	if last.Valid {
		st.LastTime = time.Unix(0, last.Int64).UTC()
	}
	if end.Valid {
		st.EndTime = time.Unix(0, end.Int64).UTC()
		st.Ended = true
	}
	return st, nil
}

// streamsOf lists one session's streams with their sizes; the caller holds
// the tx. A stream is open while its newest chunk is not final.
func streamsOf(ctx context.Context, tx *sql.Tx, session string) ([]StreamInfo, error) {
	rows, err := tx.QueryContext(ctx, `SELECT pane, stream, MAX(raw_offset + raw_length),
		SUM(LENGTH(data)), SUM(gap_before), MIN(first_time), MAX(last_time),
		MAX(is_final * (raw_offset + raw_length))
		FROM log_chunks WHERE session_id=? GROUP BY pane, stream`, session)
	if err != nil {
		return nil, fmt.Errorf("logstore: list streams: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StreamInfo
	for rows.Next() {
		var si StreamInfo
		var pane string
		var rawEnd, finalEnd sql.NullInt64
		var first, last sql.NullInt64
		if err := rows.Scan(&pane, &si.Stream, &rawEnd, &si.StoredBytes, &si.GapBytes,
			&first, &last, &finalEnd); err != nil {
			return nil, fmt.Errorf("logstore: scan stream: %w", err)
		}
		p, err := ParsePane(pane)
		if err != nil {
			return nil, err
		}
		si.Pane = p
		if rawEnd.Valid {
			si.RawBytes = uint64(rawEnd.Int64) //nolint:gosec // G115: a MAX of offsets is never negative
		}
		if first.Valid {
			si.FirstTime = time.Unix(0, first.Int64).UTC()
		}
		if last.Valid {
			si.LastTime = time.Unix(0, last.Int64).UTC()
		}
		// The stream is open while the final chunk is not the newest:
		// a final chunk always ends its stream, so finalEnd == rawEnd
		// exactly when the newest chunk is final.
		si.Open = !finalEnd.Valid || !rawEnd.Valid || finalEnd.Int64 != rawEnd.Int64
		out = append(out, si)
	}
	return out, rows.Err()
}

// summariesOf reads one session's archived pane summaries.
func summariesOf(ctx context.Context, tx *sql.Tx, session string) ([]Summary, error) {
	rows, err := tx.QueryContext(ctx, `SELECT pane, last_lines, last_output_time, activity, archived_time
		FROM archived_pane_summaries WHERE session_id=?`, session)
	if err != nil {
		return nil, fmt.Errorf("logstore: list summaries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Summary
	for rows.Next() {
		var su Summary
		var pane string
		var last, archived sql.NullInt64
		if err := rows.Scan(&pane, &su.LastLines, &last, &su.Activity, &archived); err != nil {
			return nil, fmt.Errorf("logstore: scan summary: %w", err)
		}
		p, err := ParsePane(pane)
		if err != nil {
			return nil, err
		}
		su.Pane = p
		if last.Valid {
			su.LastOutputTime = time.Unix(0, last.Int64).UTC()
		}
		if archived.Valid {
			su.ArchivedTime = time.Unix(0, archived.Int64).UTC()
		}
		out = append(out, su)
	}
	return out, rows.Err()
}

// Get returns one session's stored logs, or [ErrNotFound].
func (s *Store) Get(ctx context.Context, session string) (StoredSession, error) {
	var st StoredSession
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		var err error
		st, err = scanStored(tx.QueryRowContext(ctx, `SELECT session_id, machine_id, session_name,
			started_by_fleet, state, goals, harnesses, first_time, last_time, end_time,
			raw_bytes, stored_bytes, gap_bytes FROM log_sessions WHERE session_id=?`, session))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("logstore: get session: %w", err)
		}
		if st.Streams, err = streamsOf(ctx, tx, session); err != nil {
			return err
		}
		st.Summaries, err = summariesOf(ctx, tx, session)
		return err
	})
	return st, err
}

// Filter selects stored sessions. Empty means all.
type Filter struct {
	Machine string
	Goal    string
	State   string
	Since   time.Time
	Until   time.Time
	Limit   int
	// PageToken continues a List; empty starts over.
	PageToken string
}

// List returns stored sessions, newest last_time first, with the token for
// the next page (empty when done). The goal filter matches one goal id in
// the session's JSON goal list.
func (s *Store) List(ctx context.Context, f Filter) ([]StoredSession, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
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
	if f.PageToken != "" {
		nanos, tokSession, err := decodeToken(f.PageToken)
		if err != nil {
			return nil, "", err
		}
		conds = append(conds, "(last_time<? OR (last_time=? AND session_id>?))")
		args = append(args, nanos.UTC().UnixNano(), nanos.UTC().UnixNano(), tokSession)
	}
	q := `SELECT session_id, machine_id, session_name, started_by_fleet, state,
		goals, harnesses, first_time, last_time, end_time, raw_bytes, stored_bytes, gap_bytes
		FROM log_sessions`
	if len(conds) > 0 {
		//nolint:gosec // G202: filter clauses are fixed literals; values are bound args
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY last_time DESC, session_id ASC LIMIT ?"
	args = append(args, limit+1)
	var out []StoredSession
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return fmt.Errorf("logstore: list sessions: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var st StoredSession
			var state, goals, harnesses string
			var first, last, end sql.NullInt64
			var fleet int
			if err := rows.Scan(&st.ID, &st.Machine, &st.Name, &fleet, &state, &goals,
				&harnesses, &first, &last, &end, &st.RawBytes, &st.StoredBytes, &st.GapBytes); err != nil {
				return fmt.Errorf("logstore: scan session: %w", err)
			}
			st.StartedByFleet = fleet == 1
			st.State = state
			st.Goals = goalsOf(goals)
			st.Harnesses = goalsOf(harnesses)
			if first.Valid {
				st.FirstTime = time.Unix(0, first.Int64).UTC()
			}
			if last.Valid {
				st.LastTime = time.Unix(0, last.Int64).UTC()
			}
			if end.Valid {
				st.EndTime = time.Unix(0, end.Int64).UTC()
				st.Ended = true
			}
			if st.Streams, err = streamsOf(ctx, tx, st.ID); err != nil {
				return err
			}
			if st.Summaries, err = summariesOf(ctx, tx, st.ID); err != nil {
				return err
			}
			out = append(out, st)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	token := ""
	if len(out) > limit {
		out = out[:limit]
		token = encodeToken(out[len(out)-1].LastTime, out[len(out)-1].ID)
	}
	return out, token, nil
}

// likeEscape escapes the LIKE wildcards in a goal id.
func likeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}
