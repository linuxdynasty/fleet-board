package revocation

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// FileName is the revocation list in the state folder (02j).
const FileName = "revoked-serials"

// Header is the first line of the file.
const Header = "# fleet-board revoked-serials v1"

// maxFileBytes caps what Load reads: far above any fleet's list.
const maxFileBytes = 16 << 20

// tempPrefix starts the name of a rewrite in progress.
const tempPrefix = ".revoked-serials.tmp-"

// ErrInvalid means the file has a line that is not the format, or cannot be
// read; fleetd refuses to start (revocations_invalid). Its text names a line
// number, never the line.
var ErrInvalid = errors.New("revocation: revoked-serials is not valid")

// serialHex is a serial as big.Int.Text(16) writes one: at least 1, so no
// leading zero. Any other spelling would never match a certificate.
var serialHex = regexp.MustCompile(`^[1-9a-f][0-9a-f]{0,63}$`)

// Entry is one revoked serial (lowercase hex, ca.SerialHex) and the
// certificate's not_after.
type Entry struct {
	Serial   string
	NotAfter time.Time
}

func formatLine(e Entry) string {
	return e.Serial + " " + e.NotAfter.UTC().Format(time.RFC3339) + "\n"
}

// parsed is the content of the file.
type parsed struct {
	entries []Entry
	torn    bool // the last line had no newline and was dropped
	exists  bool
	empty   bool // the file had no complete header line
}

// readFile reads and parses path. A missing file is normal; a loose mode is
// statedb.ErrLooseMode; anything else wrong is ErrInvalid.
func readFile(path string) (parsed, error) {
	err := statedb.CheckPrivateFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return parsed{}, nil
	case errors.Is(err, statedb.ErrLooseMode):
		return parsed{}, err
	case err != nil:
		return parsed{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	f, err := os.Open(path) //nolint:gosec // G304: the fixed file name inside the operator's configured state folder
	if err != nil {
		return parsed{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return parsed{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if len(b) > maxFileBytes {
		return parsed{}, fmt.Errorf("%w: over %d bytes", ErrInvalid, maxFileBytes)
	}
	return parse(b)
}

func parse(b []byte) (parsed, error) {
	p := parsed{exists: true}
	if i := bytes.LastIndexByte(b, '\n'); i != len(b)-1 {
		p.torn = len(b) > 0
		b = b[:i+1]
	}
	if len(b) == 0 {
		p.empty = true
		return p, nil
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if lines[0] != Header {
		return parsed{}, fmt.Errorf("%w: line 1 is not the v1 header", ErrInvalid)
	}
	for i, l := range lines[1:] {
		serial, at, ok := strings.Cut(l, " ")
		t, err := time.Parse(time.RFC3339, at)
		if !ok || !serialHex.MatchString(serial) || err != nil || !strings.HasSuffix(at, "Z") {
			return parsed{}, fmt.Errorf("%w: line %d is not `<lowercase hex> <RFC 3339 UTC time>`", ErrInvalid, i+2)
		}
		p.entries = append(p.entries, Entry{Serial: serial, NotAfter: t})
	}
	return p, nil
}

// rewrite replaces the file with entries, sorted by serial: a temporary
// file (0600), fsync, rename, then an fsync of the folder.
func rewrite(dir string, entries []Entry) error {
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("revocation: temp name: %w", err)
	}
	tmp := filepath.Join(dir, tempPrefix+hex.EncodeToString(suffix))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: a fixed prefix plus random hex in the state folder
	if err != nil {
		return fmt.Errorf("revocation: create temp file: %w", err)
	}
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b Entry) int { return strings.Compare(a.Serial, b.Serial) })
	var buf strings.Builder
	buf.WriteString(Header + "\n")
	for _, e := range sorted {
		buf.WriteString(formatLine(e))
	}
	_, werr := f.WriteString(buf.String())
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return errors.Join(fmt.Errorf("revocation: write temp file: %w", err), os.Remove(tmp))
	}
	if err := os.Rename(tmp, filepath.Join(dir, FileName)); err != nil {
		return errors.Join(fmt.Errorf("revocation: replace %s: %w", FileName, err), os.Remove(tmp))
	}
	return syncDir(dir)
}

// appendEntries adds lines to the file and fsyncs it; a new file gets the
// header first, mode 0600, and an fsync of the folder.
func appendEntries(dir string, entries []Entry) error {
	path := filepath.Join(dir, FileName)
	var body strings.Builder
	for _, e := range entries {
		body.WriteString(formatLine(e))
	}
	text := body.String()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0) //nolint:gosec // G304: the fixed file name inside the state folder
	created := false
	if errors.Is(err, fs.ErrNotExist) {
		f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: as above
		created = true
		text = Header + "\n" + text
	}
	if err != nil {
		return fmt.Errorf("revocation: open %s: %w", FileName, err)
	}
	_, werr := f.WriteString(text)
	if err := errors.Join(werr, f.Sync(), f.Close()); err != nil {
		return fmt.Errorf("revocation: append to %s: %w", FileName, err)
	}
	if created {
		return syncDir(dir)
	}
	return nil
}

// removeTemps deletes rewrites a crash left behind.
func removeTemps(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("revocation: list state folder: %w", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), tempPrefix) && !e.IsDir() {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("revocation: remove leftover temp file: %w", err)
			}
		}
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // G304: the operator's configured state folder, opened only to fsync it
	if err != nil {
		return fmt.Errorf("revocation: sync state folder: %w", err)
	}
	if err := errors.Join(d.Sync(), d.Close()); err != nil {
		return fmt.Errorf("revocation: sync state folder: %w", err)
	}
	return nil
}
