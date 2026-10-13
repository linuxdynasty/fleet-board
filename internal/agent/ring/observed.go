package ring

import (
	"context"
	"database/sql"
	"fmt"
)

// Episode is one observed goal the agent tracks (02h "Observed goals"):
// the goal file's path and the first-seen time its agent-minted id hashes
// over. A row never changes except to end; a file reused for the next goal
// after DONE is a new row with a new id.
type Episode struct {
	ID        string
	Path      string
	FirstSeen int64 // Unix nanoseconds the agent first saw the file
}

// SaveEpisode records an episode. Saving the same id twice is a no-op, so
// a replayed scan never duplicates it.
func (d *DB) SaveEpisode(ctx context.Context, ep Episode) error {
	return d.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO observed_episodes(id, path, first_seen_ns, ended)
			VALUES (?, ?, ?, 0) ON CONFLICT(id) DO NOTHING`, ep.ID, ep.Path, ep.FirstSeen)
		if err != nil {
			return fmt.Errorf("ring: save observed episode: %w", err)
		}
		return nil
	})
}

// LiveEpisodes lists the episodes not yet ended, by id.
func (d *DB) LiveEpisodes(ctx context.Context) ([]Episode, error) {
	var out []Episode
	err := d.s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, path, first_seen_ns FROM observed_episodes
			WHERE ended = 0 ORDER BY id`)
		if err != nil {
			return fmt.Errorf("ring: list observed episodes: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var ep Episode
			if err := rows.Scan(&ep.ID, &ep.Path, &ep.FirstSeen); err != nil {
				return fmt.Errorf("ring: list observed episodes: %w", err)
			}
			out = append(out, ep)
		}
		return rows.Err()
	})
	return out, err
}

// EndEpisode marks an episode ended. Ending an unknown id changes nothing,
// so a replayed ending is harmless.
func (d *DB) EndEpisode(ctx context.Context, id string) error {
	return d.s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE observed_episodes SET ended = 1 WHERE id = ? AND ended = 0`, id)
		if err != nil {
			return fmt.Errorf("ring: end observed episode: %w", err)
		}
		return nil
	})
}
