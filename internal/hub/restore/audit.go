package restore

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// maxBackupName caps the backup's base name in the audit detail.
const maxBackupName = 128

// auditDetail is the closed detail of the hub.restore audit row: no path,
// no secret, nothing an operator's file name could stretch past the cap.
type auditDetail struct {
	Backup        string `json:"backup"`
	SchemaFrom    int    `json:"schema_from"`
	AddressAdded  bool   `json:"address_added"`
	SettingsEpoch string `json:"settings_epoch"`
}

// schemaVersion is the schema version of the copy at path before any
// migration runs.
func schemaVersion(ctx context.Context, path string) (int, error) {
	db, err := store.OpenReadOnly(path)
	if err != nil {
		return 0, fmt.Errorf("restore: %w", err)
	}
	defer func() { _ = db.Close() }() // read only
	var v int
	if err := db.QueryRowContext(ctx, "SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&v); err != nil {
		return 0, fmt.Errorf("restore: read the backup's schema version: %w", err)
	}
	return v, nil
}

// auditEntry is the hub.restore row (02m) for this restore, by o.Actor on
// target hub, at the restore's clock.
func auditEntry(o Options, schemaFrom int, epoch string, at time.Time) (*statedb.AuditEntry, error) {
	b, err := json.Marshal(auditDetail{
		Backup: safeName(filepath.Base(o.Backup)), SchemaFrom: schemaFrom, AddressAdded: o.Address != "", SettingsEpoch: epoch,
	})
	if err != nil {
		return nil, fmt.Errorf("restore: audit detail: %w", err)
	}
	return &statedb.AuditEntry{Actor: o.Actor, Action: statedb.ActionRestore, Target: "hub", Detail: string(b), Now: at}, nil
}

// safeName keeps letters, digits, '.', '_' and '-' of a file's base name,
// replaces every other byte with '_', and keeps at most maxBackupName
// bytes.
func safeName(name string) string {
	out := make([]byte, 0, min(len(name), maxBackupName))
	for i := 0; i < len(name) && len(out) < maxBackupName; i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
