package statedb

import (
	"context"
	"database/sql"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// writeOrdered runs apply as one writer job and returns its outcome. When
// onCommit is set, the job carries it as a post-commit hook: the writer
// runs onCommit with what the job stamped right after the batch commits,
// in commit order (#393), so the Watch publish for exactly this job's
// writes can never be inverted by a concurrent committer. A nil onCommit
// writes a plain job. The hook follows the post-commit hook rules (short,
// non-blocking, never Submit or Write); a failed job's hook never runs.
func writeOrdered[T any](ctx context.Context, s *store.Store, out *T, onCommit func(T),
	apply func(tx *sql.Tx) error,
) error {
	if onCommit == nil {
		return s.Write(ctx, apply)
	}
	return s.WriteHook(ctx, apply, func() { onCommit(*out) })
}
