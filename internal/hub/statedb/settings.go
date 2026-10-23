package statedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// settingsLayersSQL holds one row per writable settings layer
// (settings.proto), keyed by scope: "fleet" in this slice (group and
// machine layers follow with their management). The SettingsSpec rides as
// protojson, so contract changes need no migration; the version (1 at
// insert, +1 per spec change) and hub_seq (Watch order) ride beside it.
// Delivery state (SettingsStatus) is not stored: a delivery
// acknowledgement never makes an open settings form stale (02b).
const settingsLayersSQL = `CREATE TABLE settings_layers(
	scope TEXT PRIMARY KEY CHECK (scope <> ''),
	spec_json TEXT NOT NULL DEFAULT '{}',
	version INTEGER NOT NULL CHECK (version >= 1),
	create_ms INTEGER NOT NULL,
	update_ms INTEGER NOT NULL,
	hub_seq INTEGER NOT NULL DEFAULT 0
) STRICT;`

// FleetScope is the scope key of the fleet default layer, the only
// writable settings scope in this slice.
const FleetScope = "fleet"

// MachineScopeKey is the settings_layers scope of one machine's layer
// (config.proto: "the machine/<id> settings layer"). Import and enroll
// write these rows; group layers and the resolve that reads machine
// layers follow with their management.
func MachineScopeKey(machineID string) string { return "machine/" + machineID }

// ReadMachineLayer reads the SettingsSpec protojson of one machine's
// layer (found is false when the machine has none).
func ReadMachineLayer(ctx context.Context, s *store.Store, machineID string) (spec []byte, found bool, err error) {
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var raw string
		err := tx.QueryRowContext(ctx, `SELECT spec_json FROM settings_layers WHERE scope = ?`,
			MachineScopeKey(machineID)).Scan(&raw)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("statedb: read machine layer: %w", err)
		}
		spec, found = []byte(raw), true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return spec, found, nil
}

// ListMachineLayerSpecs reads every machine layer's SettingsSpec
// protojson by machine id, for the import baseline and the export.
func ListMachineLayerSpecs(ctx context.Context, s *store.Store) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT scope, spec_json FROM settings_layers
			WHERE scope LIKE 'machine/%' ORDER BY scope`)
		if err != nil {
			return fmt.Errorf("statedb: list machine layers: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var scope, raw string
			if err := rows.Scan(&scope, &raw); err != nil {
				return fmt.Errorf("statedb: list machine layers: %w", err)
			}
			out[scope[len("machine/"):]] = []byte(raw)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// upsertMachineLayerTx replaces one machine's layer, created at
// version 1 when the machine never had one. It runs inside the
// import's or the enroll's writer transaction.
func upsertMachineLayerTx(ctx context.Context, tx *sql.Tx, machineID string, spec []byte,
	now time.Time, st Stamp,
) error {
	nowMs := now.UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings_layers(scope, spec_json,
		version, create_ms, update_ms, hub_seq) VALUES (?, ?, 1, ?, ?, ?)
		ON CONFLICT (scope) DO UPDATE SET spec_json = excluded.spec_json,
		version = version + 1, update_ms = excluded.update_ms, hub_seq = excluded.hub_seq`,
		MachineScopeKey(machineID), string(spec), nowMs, nowMs, st.HubSeq); err != nil {
		return fmt.Errorf("statedb: upsert machine layer: %w", err)
	}
	return nil
}

// UpdateSettingsOp is a masked spec rewrite of the fleet layer at
// ExpectedVersion; 0 skips the check (02b "Versions and ETags"). A
// missing layer is created at version 1 (a nonzero expected_version for
// a layer that never existed is stale).
type UpdateSettingsOp struct {
	ConfigWrite
	ExpectedVersion uint64
	// Merge computes the new SettingsSpec from the stored protojson (nil
	// when the layer is new). It runs inside the writer transaction, so
	// two updates to different fields keep each other's changes. An
	// error aborts without writing.
	Merge func(current []byte) (merged []byte, err error)
}

// SettingsStored is the fleet layer after the write. Replay is set when
// the principal already used the request_id for the same request: the
// stored response comes back and nothing is written twice.
type SettingsStored struct {
	Version  uint64
	CreateMs int64
	UpdateMs int64
	HubSeq   int64
	Spec     []byte
	Replay   bool
}

// replaySettings rebuilds the stored layer of an idempotent retry.
func replaySettings(row *configLogRow) SettingsStored {
	return SettingsStored{
		Version:  uint64(row.version), //nolint:gosec // G115: versions start at 1 and only rise
		CreateMs: row.respCms, UpdateMs: row.respUms, HubSeq: row.hubSeq,
		Spec: []byte(row.spec), Replay: true,
	}
}

// ReadFleetSettings reads the fleet layer's spec and version (found is
// false when the owner never wrote it).
func ReadFleetSettings(ctx context.Context, s *store.Store) (spec []byte, version uint64, found bool, err error) {
	err = s.Read(ctx, func(tx *sql.Tx) error {
		var raw string
		var v int64
		err := tx.QueryRowContext(ctx, `SELECT spec_json, version FROM settings_layers WHERE scope = ?`,
			FleetScope).Scan(&raw, &v)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("statedb: read settings: %w", err)
		}
		spec, version, found = []byte(raw), uint64(v), true //nolint:gosec // G115: versions start at 1 and only rise
		return nil
	})
	if err != nil {
		return nil, 0, false, err
	}
	return spec, version, found, nil
}

// UpdateFleetSettings rewrites the fleet layer's masked spec in one
// writer transaction: a replayed request_id returns the stored layer, a
// stale version is [VersionStale]. A missing layer is created at version
// 1; otherwise the merged spec replaces the stored one, the version
// rises by 1 and the row is stamped, with its settings.update audit row
// (target settings/fleet, 02m).
func UpdateFleetSettings(ctx context.Context, s *store.Store, in UpdateSettingsOp, onCommit func(SettingsStored)) (SettingsStored, error) {
	jctx := context.WithoutCancel(ctx) // see insertIdentity
	var out SettingsStored
	err := writeOrdered(ctx, s, &out, onCommit, func(tx *sql.Tx) error {
		if row, err := configReplay(jctx, tx, in.ConfigWrite, configKindSettings); err != nil || row != nil {
			if err == nil {
				if err := validatedWrite(jctx); err != nil {
					return err
				}
				out = replaySettings(row)
			}
			return err
		}
		var current []byte
		var version int64
		var createMs int64
		var raw string
		rerr := tx.QueryRowContext(jctx, `SELECT spec_json, version, create_ms FROM settings_layers
			WHERE scope = ?`, FleetScope).Scan(&raw, &version, &createMs)
		switch {
		case errors.Is(rerr, sql.ErrNoRows):
			if err := checkVersion(0, in.ExpectedVersion, "settings"); err != nil {
				return err
			}
		case rerr != nil:
			return fmt.Errorf("statedb: read settings: %w", rerr)
		default:
			current = []byte(raw)
			if err := checkVersion(uint64(version), in.ExpectedVersion, "settings"); err != nil { //nolint:gosec // G115: versions start at 1 and only rise
				return err
			}
		}
		merged, err := in.Merge(current)
		if err != nil {
			return err
		}
		if err := validatedWrite(jctx); err != nil {
			return err
		}
		st, err := NextStamp(jctx, tx, in.Now)
		if err != nil {
			return err
		}
		nowMs := in.Now.UnixMilli()
		if current == nil {
			if _, err := tx.ExecContext(jctx, `INSERT INTO settings_layers(scope, spec_json,
				version, create_ms, update_ms, hub_seq) VALUES (?, ?, 1, ?, ?, ?)`,
				FleetScope, string(merged), nowMs, nowMs, st.HubSeq); err != nil {
				return fmt.Errorf("statedb: insert settings: %w", err)
			}
			out = SettingsStored{Version: 1, CreateMs: nowMs, UpdateMs: nowMs, HubSeq: st.HubSeq, Spec: merged}
		} else {
			if _, err := tx.ExecContext(jctx, `UPDATE settings_layers SET spec_json = ?,
				version = version + 1, update_ms = ?, hub_seq = ? WHERE scope = ?`,
				string(merged), nowMs, st.HubSeq, FleetScope); err != nil {
				return fmt.Errorf("statedb: update settings: %w", err)
			}
			out = SettingsStored{
				Version:  uint64(version) + 1, //nolint:gosec // G115: versions start at 1 and only rise
				CreateMs: createMs, UpdateMs: nowMs, HubSeq: st.HubSeq, Spec: merged,
			}
		}
		detail, err := auditDetail(struct {
			Scope   string `json:"scope"`
			Version uint64 `json:"version"`
		}{FleetScope, out.Version})
		if err != nil {
			return err
		}
		if _, err := AppendAudit(jctx, tx, AuditEntry{
			Actor: configActor(in.ConfigWrite), Action: ActionSettingsUpdate, Target: "settings/" + FleetScope,
			RequestID: in.RequestID, Detail: detail, Now: in.Now,
		}); err != nil {
			return err
		}
		if err := logConfigRequest(jctx, tx, in.ConfigWrite, configKindSettings, FleetScope,
			"", "", "", string(merged), int64(out.Version), //nolint:gosec // G115: versions start at 1 and only rise
			out.CreateMs, out.UpdateMs, out.HubSeq, "", false); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return SettingsStored{}, err
	}
	return out, nil
}

// SettingsLayerStored is one settings_layers row: the scope key
// ("fleet", "group/<label>" or "machine/<id>") with the stored
// SettingsSpec protojson, the version and the stamps. Group rows do
// not exist yet: nothing writes the "group/" scope, so reads miss
// them until the group management row lands.
type SettingsLayerStored struct {
	Scope    string
	Spec     []byte
	Version  uint64
	CreateMs int64
	UpdateMs int64
	HubSeq   int64
}

// scanSettingsLayer reads one settings_layers row in list order, in
// the same column order as the layer readers, so the list and the
// single reads see the same values.
func scanSettingsLayer(rows *sql.Rows) (SettingsLayerStored, error) {
	var got SettingsLayerStored
	var spec string
	var version, createMs, updateMs, hubSeq int64
	if err := rows.Scan(&got.Scope, &spec, &version, &createMs, &updateMs, &hubSeq); err != nil {
		return SettingsLayerStored{}, fmt.Errorf("statedb: read settings layer: %w", err)
	}
	got.Spec = []byte(spec)
	got.Version = uint64(version) //nolint:gosec // G115: versions start at 1 and only rise
	got.CreateMs, got.UpdateMs, got.HubSeq = createMs, updateMs, hubSeq
	return got, nil
}

// ListSettingsLayers reads every stored settings layer on the read
// pool. Fleets hold a handful of layers at most, so the API filters,
// sorts and pages in memory over this snapshot instead of building
// per-field SQL.
func ListSettingsLayers(ctx context.Context, s *store.Store) ([]SettingsLayerStored, error) {
	var out []SettingsLayerStored
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT scope, spec_json, version,
			create_ms, update_ms, hub_seq FROM settings_layers`)
		if err != nil {
			return fmt.Errorf("statedb: list settings layers: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			r, err := scanSettingsLayer(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReadSettingsLayer reads one settings layer by its scope key (found
// is false when nothing stored that scope).
func ReadSettingsLayer(ctx context.Context, s *store.Store, scope string) (SettingsLayerStored, bool, error) {
	var out SettingsLayerStored
	var found bool
	err := s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT scope, spec_json, version,
			create_ms, update_ms, hub_seq FROM settings_layers WHERE scope = ?`, scope)
		if err != nil {
			return fmt.Errorf("statedb: read settings layer: %w", err)
		}
		defer func() { _ = rows.Close() }()
		if !rows.Next() {
			return rows.Err()
		}
		r, err := scanSettingsLayer(rows)
		if err != nil {
			return err
		}
		out, found = r, true
		return rows.Err()
	})
	if err != nil {
		return SettingsLayerStored{}, false, err
	}
	return out, found, nil
}
