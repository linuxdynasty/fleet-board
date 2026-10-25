package api

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// approvalTimeoutTx resolves how long a new approval waits before its
// DENY fallback applies: the fleet layer's spec.hub.approval_timeout,
// or the documented default (defaults.md) when the owner never set one
// or set a non-positive wait the store cannot stamp. A stored value that
// fails validation (unwritable through the validated settings path)
// fails the gating call instead of guessing an expiry. Row 22's dispatch
// confirms and any later raiser must use this same helper so every kind
// shares one clock.
func approvalTimeoutTx(ctx context.Context, tx *sql.Tx) (time.Duration, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT spec_json FROM settings_layers WHERE scope = ?`,
		statedb.FleetScope).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return approvalTimeout, nil
		}
		return 0, fmt.Errorf("approvals: read approval timeout: %w", err)
	}
	var spec hubv1.SettingsSpec
	if err := protojson.Unmarshal([]byte(raw), &spec); err != nil {
		return 0, fmt.Errorf("approvals: read approval timeout: %w", err)
	}
	d := spec.GetHub().GetApprovalTimeout()
	if d == nil {
		return approvalTimeout, nil
	}
	if err := d.CheckValid(); err != nil {
		return 0, fmt.Errorf("approvals: read approval timeout: %w", err)
	}
	// The store requires expire_time after now, so a non-positive wait
	// is unstorable: fall back to the documented default instead of
	// failing every gating call on a value the settings path accepts.
	if d.AsDuration() <= 0 {
		return approvalTimeout, nil
	}
	return d.AsDuration(), nil
}
