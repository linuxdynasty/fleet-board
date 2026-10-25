package api

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// maxTimeoutSecs and maxTimeoutNanos bound the largest wait
// time.Duration can stamp: 9223372036s plus 854775807ns. Anything past
// that instant wraps AsDuration and must fail closed.
const (
	maxTimeoutSecs  = int64(math.MaxInt64) / int64(time.Second)
	maxTimeoutNanos = int32(int64(math.MaxInt64) % int64(time.Second))
)

// approvalTimeoutTx resolves how long a new approval waits before its
// DENY fallback applies: the fleet layer's spec.hub.approval_timeout,
// or the documented default (defaults.md) when the owner never set one
// or set a non-positive wait the store cannot stamp. An undecodable
// layer, or one failing CheckValid, fails the gating call instead of
// guessing an expiry; a non-positive wait (including a hand-written
// negative, which passes validation) falls back to the default per 02b.
// Row 22's dispatch confirms and any later raiser must use this same
// helper so every kind shares one clock.
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
	// time.Duration tops out near 292 years; a larger configured wait
	// cannot be stamped, and AsDuration would wrap it negative so the
	// branch below would silently substitute the default. Fail the
	// gating call instead of honoring a wait the store cannot keep.
	// The band inside the boundary second counts too: 9223372036s with
	// nanos past 854775807ns already exceeds the int64 range.
	if s := d.GetSeconds(); s > maxTimeoutSecs ||
		(s == maxTimeoutSecs && d.GetNanos() > maxTimeoutNanos) {
		return 0, fmt.Errorf("approvals: approval timeout %ds+%dns overflows time.Duration", d.GetSeconds(), d.GetNanos())
	}
	// The store requires expire_time after now, so a non-positive wait
	// is unstorable: fall back to the documented default instead of
	// failing every gating call on a value the settings path accepts.
	if d.AsDuration() <= 0 {
		return approvalTimeout, nil
	}
	return d.AsDuration(), nil
}
