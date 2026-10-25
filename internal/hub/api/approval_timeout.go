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
// that instant wraps AsDuration and must fail closed. minTimeoutSecs
// and minTimeoutNanos mirror the floor: past -9223372036s minus
// 854775808ns AsDuration wraps positive and the wait would be honored,
// contradicting the non-positive fallback promise.
const (
	maxTimeoutSecs  = int64(math.MaxInt64) / int64(time.Second)
	maxTimeoutNanos = int32(int64(math.MaxInt64) % int64(time.Second))
	minTimeoutSecs  = int64(math.MinInt64) / int64(time.Second)
	minTimeoutNanos = int32(int64(math.MinInt64) % int64(time.Second))
)

// timeoutRepresentable reports whether a (seconds, nanos) wait is one
// time.Duration can stamp: inside +/-292 years, inclusive of both
// boundary seconds. Anything outside wraps AsDuration, so it must fail
// closed at gating time and be refused at write time. Ordinary
// negatives are representable: they fall back to the default at gating
// time per 02b.
func timeoutRepresentable(seconds int64, nanos int32) bool {
	switch {
	case seconds > maxTimeoutSecs || seconds < minTimeoutSecs:
		return false
	case seconds == maxTimeoutSecs && nanos > maxTimeoutNanos:
		return false
	case seconds == minTimeoutSecs && nanos < minTimeoutNanos:
		return false
	}
	return true
}

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
	// time.Duration spans about +/-292 years; a larger configured wait
	// cannot be stamped. Past the ceiling AsDuration wraps negative so
	// the branch below would silently substitute the default; past the
	// floor it wraps positive and the wait would be honored outright,
	// contradicting the non-positive fallback promise. The bands inside
	// both boundary seconds count too. Fail the gating call instead of
	// honoring or replacing a wait the store cannot keep.
	if !timeoutRepresentable(d.GetSeconds(), d.GetNanos()) {
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
