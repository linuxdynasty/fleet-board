package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// setVersions replaces the rows of state.db's schema_migrations with
// versions.
func setVersions(t *testing.T, f *fixture, versions ...int) {
	t.Helper()
	st, err := statedb.Open(context.Background(), f.state, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = st.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(context.Background(), "DELETE FROM schema_migrations"); err != nil {
			return err
		}
		for _, v := range versions {
			if _, err := tx.ExecContext(context.Background(),
				"INSERT INTO schema_migrations(version, name, checksum, applied_time) VALUES (?, 'test', 'test', 0)", v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

// corruptPageOf breaks the b-tree page holding marker: its page-type byte
// becomes 0, which no b-tree page has.
func corruptPageOf(marker string) func(path string) error {
	return func(path string) error {
		b, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return err
		}
		size := int(binary.BigEndian.Uint16(b[16:18]))
		at := bytes.Index(b, []byte(marker))
		if size == 0 || at < size {
			return os.ErrInvalid // the marker must be past page 1
		}
		b[at/size*size] = 0
		return os.WriteFile(path, b, 0o600)
	}
}

// The check refuses a copy whose schema version this fleetd does not know
// (newer, a gap, or none) or that fails quick_check, and accepts an older
// version; a refusal is step verify, err_kind refused, and backups/ is left
// as it was.
func TestTheCheckRefusesAnUnknownSchemaOrACorruptCopy(t *testing.T) {
	t.Parallel()
	newest := len(statedb.Migrations())
	if newest < 3 {
		t.Fatalf("this test needs 3 migrations for a gap below the newest; there are %d", newest)
	}
	var newer []int // 1 to newest+1, no gap
	for v := 1; v <= newest+1; v++ {
		newer = append(newer, v)
	}
	for _, tc := range []struct {
		name     string
		versions []int // nil: leave state.db's rows as they are
		faults   backup.Faults
		ok       bool
	}{
		{name: "a version newer than this fleetd", versions: newer},
		{name: "a gap", versions: []int{1, 3}},
		{name: "no version at all", versions: []int{}},
		{name: "an older version", versions: []int{1, 2}, ok: true},
		{name: "a corrupt copy", faults: backup.Faults{AfterCopy: corruptPageOf("fleet-test")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.versions != nil {
				setVersions(t, f, tc.versions...)
			}
			res, err := backup.Take(context.Background(), backup.Options{
				StateDir: f.state, Trigger: backup.Manual, Now: func() time.Time { return stamp }, Telemetry: f.tel,
			}.WithFaults(tc.faults))
			if tc.ok {
				if err != nil {
					t.Fatalf("Take: %v", err)
				}
				assertBackupStore(t, res.Path)
				return
			}
			if err == nil {
				t.Fatalf("Take published %s", res.Path)
			}
			ev := f.logs.events(t, "backup.failed")
			if len(ev) != 1 || ev[0]["step"] != "verify" || ev[0]["err_kind"] != "refused" {
				t.Errorf("backup.failed lines = %v, want one with step verify, err_kind refused", ev)
			}
			if got := names(t, f.backups()); len(got) != 0 {
				t.Errorf("backups/ holds %q after the refusal", got)
			}
		})
	}
}
