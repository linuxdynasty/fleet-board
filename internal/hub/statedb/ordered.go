package statedb

import (
	"context"
	"database/sql"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// writeHooked runs apply as one writer job with no resource publish: the
// job captures the audit rows it stamped, and the hook publishes them
// through the store's audit publisher in batch order (#403). A failed
// job's hook never runs. Use it for audit-only writes outside
// writeOrdered (WriteAudit, import, restore) so no audit publishes late
// through the tick tail below an already-published seq.
func writeHooked(ctx context.Context, s *store.Store, apply func(tx *sql.Tx) error) error {
	var audits []AuditStored
	wrapped := func(tx *sql.Tx) error {
		start, err := counterTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := apply(tx); err != nil {
			return err
		}
		end, err := counterTx(ctx, tx)
		if err != nil {
			return err
		}
		if end > start {
			if audits, err = listAuditRangeTx(ctx, tx, start, end); err != nil {
				return err
			}
		}
		return nil
	}
	return s.WriteHook(ctx, wrapped, func() {
		if len(audits) == 0 {
			return
		}
		if v := s.AuditPublisher(); v != nil {
			if publish, ok := v.(func([]AuditStored)); ok {
				publish(audits)
			}
		}
	})
}

// writeOrdered runs apply as one writer job and returns its outcome. When
// onCommit is set, the job carries it as a post-commit hook: the writer
// runs onCommit with what the job stamped right after the batch commits,
// in commit order (#393), so the Watch publish for exactly this job's
// writes can never be inverted by a concurrent committer. A nil onCommit
// writes a plain job. The hook follows the post-commit hook rules (short,
// non-blocking, never Submit or Write); a failed job's hook never runs.
//
// The job also captures the audit rows it stamped in its tx, and the hook
// publishes them through the store's audit publisher right after the
// resource publish: per-job rows are seq-contiguous, so publishing each
// job's resource then its audits in batch order is total hub_seq order,
// and a resume never skips (#403). Unset (no publisher), the hook
// publishes the resource only.
func writeOrdered[T any](ctx context.Context, s *store.Store, out *T, onCommit func(T),
	apply func(tx *sql.Tx) error,
) error {
	if onCommit == nil {
		return s.Write(ctx, apply)
	}
	var audits []AuditStored
	wrapped := func(tx *sql.Tx) error {
		start, err := counterTx(ctx, tx)
		if err != nil {
			return err
		}
		if err := apply(tx); err != nil {
			return err
		}
		end, err := counterTx(ctx, tx)
		if err != nil {
			return err
		}
		if end > start {
			if audits, err = listAuditRangeTx(ctx, tx, start, end); err != nil {
				return err
			}
		}
		return nil
	}
	return s.WriteHook(ctx, wrapped, func() {
		onCommit(*out)
		if len(audits) == 0 {
			return
		}
		if v := s.AuditPublisher(); v != nil {
			if publish, ok := v.(func([]AuditStored)); ok {
				publish(audits)
			}
		}
	})
}
