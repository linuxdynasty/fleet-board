package statedb

import (
	"context"
	"database/sql"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// WriteHooked runs apply as one writer job with no resource publish: the
// job captures the audit rows it stamped, and the hook publishes them
// through the store's audit publisher in batch order (#403). A failed
// job's hook never runs. Use it for audit-only writes outside
// writeOrdered (WriteAudit, import, restore, approval expiry) so no audit
// publishes late through the tick tail below an already-published seq.
// Hub stores only: the range read needs hub_counters.
func WriteHooked(ctx context.Context, s *store.Store, apply func(tx *sql.Tx) error) error {
	return writeHooked(ctx, s, apply)
}

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
	return s.WriteHook(ctx, wrapped, func() { PublishAudits(s, audits) })
}

// WithAuditCapture wraps a custom-hook job's apply so its hook can publish
// exactly the audits its own tx stamped, in writer order (#403): the range
// brackets the apply in-tx, and the hook publishes the captured rows after
// its resource through the store's audit publisher. Pass the context the
// apply uses for its tx work. A failed apply captures nothing (its hook
// never runs); an apply that stamps nothing leaves audits empty.
func WithAuditCapture(ctx context.Context, apply func(tx *sql.Tx) error, audits *[]AuditStored) func(tx *sql.Tx) error {
	return func(tx *sql.Tx) (err error) {
		var start int64
		if start, err = AuditCounterTx(ctx, tx); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				return
			}
			var end int64
			if end, err = AuditCounterTx(ctx, tx); err != nil {
				return
			}
			if end > start {
				*audits, err = ListAuditRangeTx(ctx, tx, start, end)
			}
		}()
		return apply(tx)
	}
}

// PublishAudits publishes rows through the store's audit publisher, if one
// is recorded. A custom-hook job that captured its audits with
// WithAuditCapture calls it at the end of its hook, after its resource
// publish, so one writer-serialized sequence covers resources and audits in
// hub_seq order (#403). No publisher (plain unit-test stores) publishes
// nothing: tests that need the rows record their own.
func PublishAudits(s *store.Store, rows []AuditStored) {
	if len(rows) == 0 {
		return
	}
	v := s.AuditPublisher()
	if v == nil {
		return
	}
	if publish, ok := v.(func([]AuditStored)); ok {
		publish(rows)
	}
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
		PublishAudits(s, audits)
	})
}
