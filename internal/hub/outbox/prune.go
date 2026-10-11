package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Retention is how long a closed command and its result are kept after
// they closed (03f data model: commands and command_results, 30 days).
const Retention = 30 * 24 * time.Hour

// PruneEvery is how often fleetd run's housekeeping runs [Service.Prune].
const PruneEvery = time.Hour

// pruneBatch bounds the rows one prune job deletes.
const pruneBatch = 1000

// Prune is the housekeeping job "outbox_prune": it deletes the commands
// that closed (done, expired, failed) more than [Retention] before now,
// their stored results with them (ON DELETE CASCADE), in writer jobs of
// at most 1,000 rows. An open command is never pruned: it closes at its
// deadline first. It logs and counts what it removed.
func (s *Service) Prune(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-Retention).UnixMilli()
	var total int64
	defer func() {
		if total > 0 {
			s.m.pruned.Add(ctx, total)
			s.log.InfoContext(ctx, "outbox.pruned", "commands", total, "before_ms", cutoff)
		}
	}()
	for {
		var n int64
		jctx := context.WithoutCancel(ctx)
		err := s.store.Write(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(jctx, `DELETE FROM commands WHERE id IN (SELECT id FROM commands
				WHERE state IN ('done', 'expired', 'failed') AND done_time < ? LIMIT ?)`, cutoff, pruneBatch)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		if err != nil {
			return fmt.Errorf("outbox: prune: %w", err)
		}
		total += n
		if n < pruneBatch {
			return nil
		}
	}
}
