package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// StoredCommand is one outbox row for the resend throttle: the command
// as enqueued, its lifecycle state (pending, delivered, done, expired,
// failed) and when it was created.
type StoredCommand struct {
	Command *agentv1.Command
	State   string
	Created time.Time
}

// RecentCommands lists machine's kind commands created at or after
// since, oldest first, for callers that must not re-send what is
// already in flight (the hook reconcile's once-a-day rule). It reads
// inside the caller's writer job (tx), so the decision and the enqueue
// it guards commit together.
func RecentCommands(ctx context.Context, tx *sql.Tx, machine, kind string, since time.Time) ([]StoredCommand, error) {
	rows, err := tx.QueryContext(ctx, `SELECT command, state, create_time FROM commands
		WHERE machine_id = ? AND kind = ? AND create_time >= ? ORDER BY id`, machine, kind, since.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("outbox: list recent commands: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StoredCommand
	for rows.Next() {
		var raw []byte
		var created int64
		var sc StoredCommand
		if err := rows.Scan(&raw, &sc.State, &created); err != nil {
			return nil, fmt.Errorf("outbox: scan recent command: %w", err)
		}
		sc.Created = time.UnixMilli(created).UTC()
		var cmd agentv1.Command
		if err := proto.Unmarshal(raw, &cmd); err != nil {
			return nil, fmt.Errorf("outbox: decode recent command: %w", err)
		}
		sc.Command = &cmd
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox: list recent commands: %w", err)
	}
	return out, nil
}
