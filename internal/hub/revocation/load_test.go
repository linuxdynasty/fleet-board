package revocation_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func TestAMissingFileIsNormalAndIsNotCreated(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil, nil)
	l, err := f.load(t, now)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if l.Revoked("ab") {
		t.Error("an empty list revokes ab")
	}
	if _, err := os.Lstat(filepath.Join(f.dir, revocation.FileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load created the file: %v", err)
	}
}

// At start the file and state.db are reconciled both ways, and expired
// lines are dropped by an atomic rewrite (sorted by serial).
func TestLoadReconcilesTheFileAndTheStore(t *testing.T) {
	t.Parallel()
	live, gone := now.Add(48*time.Hour), now.Add(-time.Hour)
	rows := []serialRow{
		{serial: "a1", notAfter: live, revoked: now.Add(-time.Hour)}, // revoked in state.db only
		{serial: "b2", notAfter: live},                               // revoked in the file only
		{serial: "c3", notAfter: live},                               // never revoked
	}
	file := lines(header, line("b2", live), line("dd", gone), line("e5", live))
	f := newFixture(t, rows, ptr(file))
	l, err := f.load(t, now)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for serial, want := range map[string]bool{"a1": true, "b2": true, "e5": true, "c3": false, "dd": false} {
		if got := l.Revoked(serial); got != want {
			t.Errorf("Revoked(%s) = %v, want %v", serial, got, want)
		}
	}
	if got, want := f.file(t), lines(header, line("a1", live), line("b2", live), line("e5", live)); got != want {
		t.Errorf("file after Load:\n%s\nwant:\n%s", got, want)
	}
	if got := f.revokedInStore(t); !reflect.DeepEqual(got, []string{"a1", "b2"}) {
		t.Errorf("state.db revoked %v, want [a1 b2] (e5 is unknown to state.db, the file keeps it)", got)
	}
	fi, err := os.Stat(filepath.Join(f.dir, revocation.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %04o, want 0600", fi.Mode().Perm())
	}
	entries, err := os.ReadDir(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "revoked-serials.") {
			t.Errorf("Load left %s behind", e.Name())
		}
	}
}

func TestATornLastLineIsDroppedWithAWarning(t *testing.T) {
	t.Parallel()
	live := now.Add(48 * time.Hour)
	cases := map[string]struct{ file, want string }{
		"a torn entry":     {lines(header, line("ab", live), "cd 2026-10-0"), lines(header, line("ab", live))},
		"a torn header":    {"# fleet-board revo", header},
		"an empty file":    {"", header},
		"a header no line": {strings.TrimSuffix(header, "\n"), header},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, nil, ptr(tc.file))
			l, err := f.load(t, now)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if l.Revoked("cd") {
				t.Error("the torn serial was kept")
			}
			if got := f.file(t); got != tc.want {
				t.Errorf("file %q, want %q", got, tc.want)
			}
			torn := strings.Contains(f.logs.String(), `"msg":"hub.revocations_torn_line"`)
			if want := tc.file != ""; torn != want {
				t.Errorf("hub.revocations_torn_line logged %v, want %v: %s", torn, want, f.logs.String())
			}
		})
	}
}

func TestABadFileRefusesToStart(t *testing.T) {
	t.Parallel()
	live := now.Add(48 * time.Hour).Format(time.RFC3339)
	cases := map[string]string{
		"another header":        "# fleet-board revoked-serials v2\n",
		"no header":             "ab " + live + "\n",
		"uppercase serial":      header + "AB " + live + "\n",
		"not hex":               header + "xy " + live + "\n",
		"a leading zero":        header + "0ab " + live + "\n",
		"a zero serial":         header + "0 " + live + "\n",
		"a time not in UTC":     header + "ab " + now.Add(48*time.Hour).In(time.FixedZone("x", 3600)).Format(time.RFC3339) + "\n",
		"no time":               header + "ab\n",
		"bad time":              header + "ab 2026-13-01T00:00:00Z\n",
		"a third field":         header + "ab " + live + " extra\n",
		"a blank line":          header + "\nab " + live + "\n",
		"two spaces":            header + "ab  " + live + "\n",
		"a bad line, then torn": header + "zz\nab " + live,
	}
	for name, file := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, nil, ptr(file))
			_, err := f.load(t, now)
			if !errors.Is(err, revocation.ErrInvalid) {
				t.Fatalf("Load = %v, want ErrInvalid", err)
			}
			if strings.Contains(err.Error(), live) {
				t.Error("the error echoes the file")
			}
			if got := f.file(t); got != file {
				t.Error("Load changed a file it refused")
			}
		})
	}
}

func TestALooseOrUnreadableFileIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil, ptr(header))
	path := filepath.Join(f.dir, revocation.FileName)
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: loosened on purpose; Load must refuse it
		t.Fatal(err)
	}
	if _, err := f.load(t, now); !errors.Is(err, statedb.ErrLooseMode) {
		t.Errorf("a 0644 file: %v, want statedb.ErrLooseMode", err)
	}
	g := newFixture(t, nil, nil)
	if err := os.Mkdir(filepath.Join(g.dir, revocation.FileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := g.load(t, now); !errors.Is(err, revocation.ErrInvalid) {
		t.Errorf("a folder in its place: %v, want ErrInvalid", err)
	}
}

func TestLoadRemovesARewriteLeftOverByACrash(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil, ptr(header))
	tmp := filepath.Join(f.dir, ".revoked-serials.tmp-0123456789abcdef")
	if err := os.WriteFile(tmp, []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.load(t, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the leftover temp file is still there: %v", err)
	}
}
