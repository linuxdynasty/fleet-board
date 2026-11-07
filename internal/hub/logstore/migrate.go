package logstore

import "github.com/linuxdynasty/fleet-board/internal/store"

// Migrations is the forward-only logs.db schema. Never edit an entry that
// has run anywhere; add the next version.
func Migrations() []store.Migration {
	return []store.Migration{
		{
			Version: 1,
			Name:    "log sessions, chunks and archived summaries",
			SQL: `CREATE TABLE log_sessions(
				session_id TEXT PRIMARY KEY,
				machine_id TEXT NOT NULL,
				session_name TEXT NOT NULL DEFAULT '',
				started_by_fleet INTEGER NOT NULL DEFAULT 0,
				state TEXT NOT NULL DEFAULT 'LIVE',
				goals TEXT NOT NULL DEFAULT '[]',
				harnesses TEXT NOT NULL DEFAULT '[]',
				first_time INTEGER,
				last_time INTEGER,
				end_time INTEGER,
				raw_bytes INTEGER NOT NULL DEFAULT 0,
				stored_bytes INTEGER NOT NULL DEFAULT 0,
				gap_bytes INTEGER NOT NULL DEFAULT 0
			);
			CREATE TABLE log_chunks(
				session_id TEXT NOT NULL REFERENCES log_sessions(session_id),
				pane TEXT NOT NULL,
				stream INTEGER NOT NULL,
				raw_offset INTEGER NOT NULL,
				raw_length INTEGER NOT NULL,
				frame_start INTEGER NOT NULL DEFAULT 0,
				frame_raw_offset INTEGER NOT NULL DEFAULT 0,
				first_time INTEGER,
				last_time INTEGER,
				gap_before INTEGER NOT NULL DEFAULT 0,
				is_final INTEGER NOT NULL DEFAULT 0,
				goal_id TEXT NOT NULL DEFAULT '',
				data BLOB NOT NULL,
				PRIMARY KEY (session_id, pane, stream, raw_offset)
			);
			CREATE INDEX log_chunks_session ON log_chunks(session_id);
			CREATE TABLE archived_pane_summaries(
				session_id TEXT NOT NULL REFERENCES log_sessions(session_id),
				pane TEXT NOT NULL,
				last_lines TEXT NOT NULL DEFAULT '',
				last_output_time INTEGER,
				activity INTEGER NOT NULL DEFAULT 0,
				archived_time INTEGER NOT NULL,
				PRIMARY KEY (session_id, pane)
			);
			CREATE TABLE log_meta(
				key TEXT PRIMARY KEY,
				value INTEGER NOT NULL DEFAULT 0
			);`,
		},
	}
}
