package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// MachineRow is one machines row as the hub stored it: the enroll-time
// identity and labels, the connection state, and the last Snapshot facts.
// The Machines view (hub API reads, row 12) filters, sorts and pages on
// these columns; the proto assembly lives with the API, so this package
// stays free of generated types.
type MachineRow struct {
	MachineID string
	Name      string
	Labels    []string
	// Drained is the hub's drain request (enroll start_drained until row
	// 24 builds DrainMachine); AgentDraining is what the agent reported.
	Drained        bool
	AgentDraining  bool
	InstallID      string
	State          string
	Facts          string
	CreatedMillis  int64
	Version        int64
	ObservedMillis int64
	HubSeq         int64
	// LastSeenMillis is the last heartbeat, 0 when never connected.
	LastSeenMillis int64
	// Resources is the last ResourceSample as protojson, empty when the
	// machine never sent one. Policy is the last Snapshot's policy row,
	// and Shed, Coalesced and Evicted are its Hello event counters.
	Resources                 string
	PolicyHash, PolicyError   string
	PolicyChanged, PolicySafe bool
	Shed, Coalesced, Evicted  int64
}

// machineColumns lists every machines column a MachineRow scan reads.
const machineColumns = `machine_id, name, labels, drained, agent_draining, install_id,
	state, facts, created_time, version, observed_time, hub_seq, last_seen_time,
	resources, policy_hash, policy_changed_since_load, policy_safe_mode, policy_error,
	events_shed_total, events_coalesced_total, events_evicted_total`

// rowScanner is one scanned machines row, single or from a result set.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanMachineRow(row rowScanner) (MachineRow, error) {
	var (
		m                     MachineRow
		labels                string
		policyHash, policyErr string
		drained, agent        int64
		changed, safe         int64
		seen                  sql.NullInt64
		res                   sql.NullString
	)
	err := row.Scan(&m.MachineID, &m.Name, &labels, &drained, &agent, &m.InstallID,
		&m.State, &m.Facts, &m.CreatedMillis, &m.Version, &m.ObservedMillis, &m.HubSeq, &seen,
		&res, &policyHash, &changed, &safe, &policyErr,
		&m.Shed, &m.Coalesced, &m.Evicted)
	if errors.Is(err, sql.ErrNoRows) {
		return MachineRow{}, ErrMachineUnknown
	}
	if err != nil {
		return MachineRow{}, fmt.Errorf("statedb: read machine row: %w", err)
	}
	if err := json.Unmarshal([]byte(labels), &m.Labels); err != nil {
		return MachineRow{}, fmt.Errorf("statedb: machine labels: %w", err)
	}
	m.Drained, m.AgentDraining = drained != 0, agent != 0
	if seen.Valid {
		m.LastSeenMillis = seen.Int64
	}
	if res.Valid {
		m.Resources = res.String
	}
	m.PolicyHash, m.PolicyError = policyHash, policyErr
	m.PolicyChanged, m.PolicySafe = changed != 0, safe != 0
	return m, nil
}

// ReadMachineRow reads one machine row on the read pool
// ([ErrMachineUnknown] when there is no row).
func ReadMachineRow(ctx context.Context, s *store.Store, id string) (MachineRow, error) {
	var out MachineRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var err error
		out, err = scanMachineRow(tx.QueryRowContext(ctx,
			`SELECT `+machineColumns+` FROM machines WHERE machine_id = ?`, id))
		return err
	})
	return out, err
}

// ListMachineRows reads every machine row on the read pool. Fleets hold
// tens of machines, so the API filters, sorts and pages in memory over
// this snapshot instead of building per-field SQL.
func ListMachineRows(ctx context.Context, s *store.Store) ([]MachineRow, error) {
	var out []MachineRow
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+machineColumns+` FROM machines`)
		if err != nil {
			return fmt.Errorf("statedb: list machine rows: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			m, err := scanMachineRow(rows)
			if err != nil {
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
