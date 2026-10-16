package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// pendingMachineSQL is migration 20 (plan row 13a, issue #48 criterion
// 21): the machine entries ImportConfig keeps until a machine enrolls
// under the name (02m "Config export and import"). One row per pending
// name: the labels and the machine settings layer's values (a
// SettingsSpec as protojson with the scope unset; the scope column of
// settings_layers names the machine once it enrolls) with the usual
// millisecond times and the Watch stamp. A bare name still needs its
// row: applying it at enroll records the machine drained, even when it
// carries no labels and no values.
const pendingMachineSQL = `CREATE TABLE pending_machines(
	name TEXT PRIMARY KEY CHECK (name <> ''),
	labels TEXT NOT NULL DEFAULT '[]',
	values_json TEXT NOT NULL DEFAULT '{}',
	create_ms INTEGER NOT NULL,
	update_ms INTEGER NOT NULL,
	hub_seq INTEGER NOT NULL DEFAULT 0 CHECK (hub_seq >= 0)
) STRICT;`

// PendingMachine is one pending_machines row: the labels and the
// SettingsSpec protojson (scope unset) an import keeps for a name no
// machine has yet.
type PendingMachine struct {
	Name     string
	Labels   []string
	Values   []byte
	CreateMs int64
	UpdateMs int64
	HubSeq   int64
}

// MachineIdentity is the enrolled-machine baseline an import plan
// diffs machine entries against: the id (for the settings-layer
// scope), the display name, the labels, and the version (so the apply
// can refuse a machine an owner write moved since the dry run).
type MachineIdentity struct {
	MachineID string
	Name      string
	Labels    []string
	Version   uint64
}

// parseLabels parses a labels JSON column; a corrupt stored row is the
// hub's own failure, never the caller's.
func parseLabels(raw string) ([]string, error) {
	var labels []string
	if err := json.Unmarshal([]byte(raw), &labels); err != nil {
		return nil, fmt.Errorf("statedb: machine labels: %w", err)
	}
	if labels == nil {
		labels = []string{}
	}
	return labels, nil
}

// marshalLabels renders labels for storage; nil rides as an empty list
// so a read always parses.
func marshalLabels(labels []string) (string, error) {
	if labels == nil {
		labels = []string{}
	}
	raw, err := json.Marshal(labels)
	if err != nil {
		return "", fmt.Errorf("statedb: machine labels: %w", err)
	}
	return string(raw), nil
}

// hasPendingValues reports whether a pending entry's SettingsSpec
// protojson carries any values. The import layer always writes the
// canonical protojson encoding, whose empty message is exactly "{}",
// so a byte comparison is enough and the store stays free of proto
// types: an empty layer is never written.
func hasPendingValues(spec []byte) bool {
	return len(spec) > 0 && string(spec) != "{}"
}

// MergeEnrollLabels is the label merge at enroll (02m): the token's
// labels with a matching pending entry's, deduplicated and sorted so
// the stored list is stable. The token JSON comes from enroll_tokens;
// a corrupt stored row is the hub's own failure.
func MergeEnrollLabels(tokenJSON string, pending []string) ([]string, error) {
	token, err := parseLabels(tokenJSON)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, l := range append(token, pending...) {
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ListMachineIdentities reads every enrolled machine's id, display
// name and labels in id order, for the import baseline and the export.
func ListMachineIdentities(ctx context.Context, s *store.Store) ([]MachineIdentity, error) {
	var out []MachineIdentity
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT machine_id, name, labels, version FROM machines ORDER BY machine_id`)
		if err != nil {
			return fmt.Errorf("statedb: list machines: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var m MachineIdentity
			var labels string
			var version int64
			if err := rows.Scan(&m.MachineID, &m.Name, &labels, &version); err != nil {
				return fmt.Errorf("statedb: list machines: %w", err)
			}
			m.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
			if m.Labels, err = parseLabels(labels); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListPendingMachines reads every pending machine in name order, for
// the import baseline and the export.
func ListPendingMachines(ctx context.Context, s *store.Store) ([]PendingMachine, error) {
	var out []PendingMachine
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT name, labels, values_json, create_ms, update_ms, hub_seq
			FROM pending_machines ORDER BY name`)
		if err != nil {
			return fmt.Errorf("statedb: list pending machines: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				p      PendingMachine
				labels string
				values string
				hubSeq int64
			)
			if err := rows.Scan(&p.Name, &labels, &values, &p.CreateMs, &p.UpdateMs, &hubSeq); err != nil {
				return fmt.Errorf("statedb: list pending machines: %w", err)
			}
			if p.Labels, err = parseLabels(labels); err != nil {
				return err
			}
			p.Values = []byte(values)
			p.HubSeq = hubSeq
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// upsertPendingTx stores one import's machine entry for a name no
// machine has, stamped like every owner write. It runs inside the
// import's writer transaction.
func upsertPendingTx(ctx context.Context, tx *sql.Tx, name, labels, values string, now time.Time, st Stamp) error {
	nowMs := now.UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO pending_machines(name, labels, values_json,
		create_ms, update_ms, hub_seq) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET labels = excluded.labels, values_json = excluded.values_json,
		update_ms = excluded.update_ms, hub_seq = excluded.hub_seq`,
		name, labels, values, nowMs, nowMs, st.HubSeq); err != nil {
		return fmt.Errorf("statedb: upsert pending machine: %w", err)
	}
	return nil
}

// consumePendingTx reads and deletes the pending entry for an
// enrolling name, in the enroll's writer transaction. found is false
// when no import kept the name.
func consumePendingTx(ctx context.Context, tx *sql.Tx, name string) (p PendingMachine, found bool, err error) {
	var (
		labels, values string
		hubSeq         int64
	)
	err = tx.QueryRowContext(ctx, `SELECT name, labels, values_json, create_ms, update_ms, hub_seq
		FROM pending_machines WHERE name = ?`, name).
		Scan(&p.Name, &labels, &values, &p.CreateMs, &p.UpdateMs, &hubSeq)
	switch {
	case err == nil:
		if p.Labels, err = parseLabels(labels); err != nil {
			return PendingMachine{}, false, err
		}
		p.Values = []byte(values)
		p.HubSeq = hubSeq
	case errors.Is(err, sql.ErrNoRows):
		return PendingMachine{}, false, nil
	default:
		return PendingMachine{}, false, fmt.Errorf("statedb: read pending machine: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_machines WHERE name = ?`, name); err != nil {
		return PendingMachine{}, false, fmt.Errorf("statedb: consume pending machine: %w", err)
	}
	return p, true, nil
}

// applyMachineLabelsTx replaces one enrolled machine's labels the way
// UpdateMachine does (the version rises by 1, the row is stamped) but
// without its idempotency record: the import's own record owns the
// retry. Import never touches the drain flag. It runs inside the
// import's writer transaction.
func applyMachineLabelsTx(ctx context.Context, tx *sql.Tx, machineID string, labels []string,
	now time.Time, st Stamp,
) error {
	list, err := marshalLabels(labels)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE machines SET labels = ?, version = version + 1,
		update_ms = ?, observed_time = ?, hub_seq = ? WHERE machine_id = ?`,
		list, now.UnixMilli(), st.ObservedTime.UnixMilli(), st.HubSeq, machineID)
	if err != nil {
		return fmt.Errorf("statedb: apply machine labels: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return fmt.Errorf("statedb: apply machine labels: %w", err)
		}
		return ErrMachineUnknown
	}
	return nil
}
