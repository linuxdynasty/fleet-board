package statedb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/ulid"
)

// systemItemExpiry is how long a SYSTEM triage item lives after its raise
// (the 7-day triage expiry of 03d). Updates keep the raise's expiry; only
// a fresh raise stamps a new one.
const systemItemExpiry = 7 * 24 * time.Hour

// systemSeverityName renders a SYSTEM severity for triage_items: the
// SEVERITY_ prefix off, so SEVERITY_WARNING reads WARNING like the other
// triage rows.
func systemSeverityName(s hubv1.Severity) string {
	return strings.TrimPrefix(s.String(), "SEVERITY_")
}

// systemSeverityOf parses a stored SYSTEM severity back. A row the hub
// cannot read is the hub's failure, reported, never guessed.
func systemSeverityOf(name string) (hubv1.Severity, error) {
	if v, ok := hubv1.Severity_value["SEVERITY_"+name]; ok {
		return hubv1.Severity(v), nil
	}
	return hubv1.Severity_SEVERITY_UNSPECIFIED,
		fmt.Errorf("statedb: unknown system severity %q", name)
}

// ListOpenSystemItems reads the currently-open SYSTEM triage items as the
// open list behind system.Decide: one entry per row in OPEN or SNOOZED, in
// fingerprint order. A snoozed item is still open (its wake time only
// pauses the owner's view), so the evaluator keeps updating it in place.
func ListOpenSystemItems(ctx context.Context, s *store.Store) ([]system.OpenItem, error) {
	var out []system.OpenItem
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT fingerprint, severity, detail_json FROM triage_items
			WHERE kind = 'SYSTEM' AND state IN ('OPEN', 'SNOOZED') ORDER BY fingerprint`)
		if err != nil {
			return fmt.Errorf("statedb: list open system items: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var fingerprint, severityName, detailJSON string
			if err := rows.Scan(&fingerprint, &severityName, &detailJSON); err != nil {
				return fmt.Errorf("statedb: scan open system item: %w", err)
			}
			severity, err := systemSeverityOf(severityName)
			if err != nil {
				return err
			}
			var detail hubv1.SystemDetail
			if err := protojson.Unmarshal([]byte(detailJSON), &detail); err != nil {
				return fmt.Errorf("statedb: decode system detail for %q: %w", fingerprint, err)
			}
			out = append(out, system.OpenItem{
				Fingerprint: fingerprint, Severity: severity, Detail: &detail,
			})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ApplySystemChanges persists one round of system.Decide writes in one
// writer transaction: raises open new SYSTEM rows at version 1 with a
// 7-day expiry, updates move the open row in place at version + 1, and
// retires close it. A row that left OPEN or SNOOZED since the round was
// decided (snoozed, dismissed, already retired) is skipped: the next round
// re-decides from the fresh open list, so nothing is lost and nothing is
// written twice. Like the other triage raises it writes no audit row.
func ApplySystemChanges(ctx context.Context, s *store.Store, changes []system.Change, now time.Time) error {
	if len(changes) == 0 {
		return nil
	}
	if now.IsZero() {
		return ErrZeroTime
	}
	jctx := context.WithoutCancel(ctx)
	return s.Write(ctx, func(tx *sql.Tx) error {
		for _, c := range changes {
			st, err := NextStamp(jctx, tx, now)
			if err != nil {
				return err
			}
			switch c.Op {
			case system.OpRaise:
				if err := raiseSystemItem(jctx, tx, c, now, st.HubSeq); err != nil {
					return err
				}
			case system.OpUpdate:
				if err := updateSystemItem(jctx, tx, c, now, st.HubSeq); err != nil {
					return err
				}
			case system.OpRetire:
				if err := retireSystemItem(jctx, tx, c, now, st.HubSeq); err != nil {
					return err
				}
			default:
				return fmt.Errorf("statedb: unknown system change op %d for %q", int(c.Op), c.Fingerprint)
			}
		}
		return nil
	})
}

// raiseSystemItem opens one SYSTEM row: a fresh ULID, version 1, the
// fingerprint, title, subject ids (an integration subject rides in the
// detail: triage items have no integration column) and the SystemDetail as
// JSON, expiring 7 days after the raise.
func raiseSystemItem(ctx context.Context, tx *sql.Tx, c system.Change, now time.Time, hubSeq int64) error {
	if c.Fingerprint == "" || c.Title == "" || c.Severity == hubv1.Severity_SEVERITY_UNSPECIFIED {
		return fmt.Errorf("%w: a system raise needs a fingerprint, a severity and a title", ErrTriageInvalid)
	}
	if c.Detail == nil {
		return fmt.Errorf("%w: a system raise needs its SystemDetail", ErrTriageInvalid)
	}
	detailJSON, err := protojson.Marshal(c.Detail)
	if err != nil {
		return fmt.Errorf("statedb: encode system detail for %q: %w", c.Fingerprint, err)
	}
	id, err := ulid.New(now, rand.Reader)
	if err != nil {
		return fmt.Errorf("statedb: mint system item id: %w", err)
	}
	nowMs := now.UnixMilli()
	severity := systemSeverityName(c.Severity)
	if _, err := tx.ExecContext(ctx, `INSERT INTO triage_items(id, version, kind, severity, state,
		fingerprint, title, machine_id, session_id, goal_id, account_id,
		detail_kind, detail_json, expire_ms, create_ms, update_ms, hub_seq)
		VALUES (?, 1, 'SYSTEM', ?, 'OPEN', ?, ?, ?, '', '', ?, 'system', ?, ?, ?, ?, ?)`,
		id, severity, c.Fingerprint, c.Title,
		c.Machine.GetValue(), c.Account.GetValue(),
		string(detailJSON), now.Add(systemItemExpiry).UnixMilli(), nowMs, nowMs, hubSeq); err != nil {
		return fmt.Errorf("statedb: raise system item %q: %w", c.Fingerprint, err)
	}
	return nil
}

// updateSystemItem moves the open SYSTEM row in place: severity, title and
// detail at version + 1, stamped. The state (OPEN or SNOOZED) is kept: a
// snooze pauses the owner's view, not the evaluator. No open row means
// someone closed it since the round was decided; that is left alone.
func updateSystemItem(ctx context.Context, tx *sql.Tx, c system.Change, now time.Time, hubSeq int64) error {
	if c.Detail == nil {
		return fmt.Errorf("%w: a system update needs its SystemDetail", ErrTriageInvalid)
	}
	detailJSON, err := protojson.Marshal(c.Detail)
	if err != nil {
		return fmt.Errorf("statedb: encode system detail for %q: %w", c.Fingerprint, err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE triage_items
		SET severity = ?, title = ?, detail_json = ?, version = version + 1, update_ms = ?, hub_seq = ?
		WHERE fingerprint = ? AND state IN ('OPEN', 'SNOOZED')`,
		systemSeverityName(c.Severity), c.Title, string(detailJSON),
		now.UnixMilli(), hubSeq, c.Fingerprint); err != nil {
		return fmt.Errorf("statedb: update system item %q: %w", c.Fingerprint, err)
	}
	return nil
}

// retireSystemItem closes the open SYSTEM row: state RETIRED at version +
// 1, stamped, its snooze wake time cleared. No open row means someone
// already closed it; that is left alone.
func retireSystemItem(ctx context.Context, tx *sql.Tx, c system.Change, now time.Time, hubSeq int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE triage_items
		SET state = 'RETIRED', snoozed_until_ms = 0, version = version + 1, update_ms = ?, hub_seq = ?
		WHERE fingerprint = ? AND state IN ('OPEN', 'SNOOZED')`,
		now.UnixMilli(), hubSeq, c.Fingerprint); err != nil {
		return fmt.Errorf("statedb: retire system item %q: %w", c.Fingerprint, err)
	}
	return nil
}
