package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
)

// releasePrompt clears the in-flight answer when its SendKeys ends (a
// result lands, the deadline passes, the machine is revoked), so the next
// answer for that prompt can go (03d W7). Other kinds hold nothing. A
// missing command row or an answer key the hub never held is fine.
func releasePrompt(ctx context.Context, tx *sql.Tx, machineID, commandID string) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT command FROM commands WHERE command_id = ?`, commandID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("outbox: release prompt: %w", err)
	}
	var cmd agentv1.Command
	if err := proto.Unmarshal(raw, &cmd); err != nil {
		return fmt.Errorf("outbox: release prompt: %w", err)
	}
	prompt := cmd.GetSendKeys().GetAnsweringPrompt().GetValue()
	if prompt == "" {
		return nil
	}
	return approvals.ClearInflight(ctx, tx, machineID, prompt)
}
