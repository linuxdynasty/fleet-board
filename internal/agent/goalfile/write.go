package goalfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"syscall"

	"golang.org/x/sys/unix"
)

var (
	// ErrDirNotPlain means .fleet-board is a symlink, a plain file, or
	// otherwise not a plain directory. Nothing was changed. Merge seam:
	// this must unify with internal/agent/worktree.ErrDirNotPlain (lane
	// 1) when both lanes land; the names and contract match so the merge
	// deletes this copy and imports that package.
	ErrDirNotPlain = errors.New("goalfile: .fleet-board is not a plain directory")
	// ErrDirForeign means .fleet-board/.gitignore is not the marker the
	// agent wrote. Nothing was changed. Merge seam: unifies with
	// internal/agent/worktree.ErrDirForeign the same way.
	ErrDirForeign = errors.New("goalfile: .fleet-board/.gitignore is foreign")
	// ErrGoalFileExists means goal-<id> already holds other content: a
	// repeated write is only declarative for the same bytes.
	ErrGoalFileExists = errors.New("goalfile: the goal file already holds other content")
	// ErrGoalID means the goal id is not a safe file name.
	ErrGoalID = errors.New("goalfile: the goal id is not a safe file name")
	// ErrBoardUnwired means no board bootstrap was injected: the row 20
	// lane-1 helper (internal/agent/worktree.EnsureBoardDir) lands at
	// merge, and until then every file step refuses instead of guessing.
	ErrBoardUnwired = errors.New("goalfile: the board bootstrap is unwired")
	// ErrGoalFileUnsafe means goal-<id> itself is a symlink, not a
	// regular file, has more than one link, or is over the read cap. It
	// is never followed, replaced or read. It carries no Why metadata:
	// only the two folder refusals do.
	ErrGoalFileUnsafe = errors.New("goalfile: the goal file is not a plain file")
)

// goalIDRule is the ids.proto id rule the file name relies on: 1 to 32
// characters of a-z, 0-9 and '-', starting with a letter or digit.
var goalIDRule = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidGoalID reports whether id is a safe goal file name and registry id.
func ValidGoalID(id string) bool { return goalIDRule.MatchString(id) }

// maxReadBytes caps a goal-file read (01d2 step 5, 03k: 4 KiB).
const maxReadBytes = 4096

// Why maps a write refusal to its GOAL_FILE_WRITE_FAILED metadata value
// (01d2): "goal_dir_not_plain" or "goal_dir_foreign", else "".
func Why(err error) string {
	switch {
	case errors.Is(err, ErrDirNotPlain):
		return "goal_dir_not_plain"
	case errors.Is(err, ErrDirForeign):
		return "goal_dir_foreign"
	default:
		return ""
	}
}

// Writer writes, reads and removes generic goal files
// (<checkout top>/.fleet-board/goal-<goal_id>, 01d2). EnsureBoard is the
// lane-1 bootstrap (internal/agent/worktree.EnsureBoardDir in
// production): it creates .fleet-board with its self-ignoring marker
// through no-follow opens and refuses a symlink, a file or a foreign
// marker. Everything below takes the returned board path and never
// walks it twice: the board directory is re-opened no-follow, and the
// goal file is reached only through that handle (unix.Openat).
type Writer struct {
	EnsureBoard func(workdir string) (board string, created bool, err error)
}

// EnsureGoalFile writes content as .fleet-board/goal-<goal_id> (mode
// 0600, O_EXCL|O_NOFOLLOW): a repeated call with the same bytes is OK,
// other bytes are refused, and a symlink in the name's place is never
// followed or replaced. With repoShipsOwn (an eshu repo: 01d) it writes
// nothing and reports skipped. A refused board maps to ErrDirNotPlain
// or ErrDirForeign and changes nothing.
func (w Writer) EnsureGoalFile(workdir, goalID string, content []byte, repoShipsOwn bool) (bool, error) {
	if repoShipsOwn {
		return true, nil
	}
	if !goalIDRule.MatchString(goalID) {
		return false, fmt.Errorf("goalfile: write %q: %w", goalID, ErrGoalID)
	}
	board, _, err := w.board(workdir)
	if err != nil {
		return false, err
	}
	dir, err := openBoard(board)
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(dir) }()
	name := "goal-" + goalID
	fd, err := unix.Openat(dir, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err == nil {
		werr := writeClose(fd, content)
		if werr != nil {
			return false, fmt.Errorf("goalfile: write %s: %w", name, werr)
		}
		return false, nil
	}
	if !errors.Is(err, syscall.EEXIST) {
		return false, fmt.Errorf("goalfile: create %s: %w", name, notPlain(err))
	}
	same, rerr := sameContent(dir, name, content)
	if rerr != nil {
		return false, fmt.Errorf("goalfile: read %s: %w", name, rerr)
	}
	if !same {
		return false, fmt.Errorf("goalfile: %s holds %w", name, ErrGoalFileExists)
	}
	return false, nil
}

// ReleaseGoalFile unlinks goal-<goal_id> through the board handle (no
// follow) and then removes .fleet-board/ only when its .gitignore is
// the single entry left; a folder holding anything else stays (01d2
// "Cleanup"). A missing file or folder is OK (declarative).
func (w Writer) ReleaseGoalFile(workdir, goalID string) error {
	if !goalIDRule.MatchString(goalID) {
		return fmt.Errorf("goalfile: release %q: %w", goalID, ErrGoalID)
	}
	board, _, err := w.board(workdir)
	if err != nil {
		return err
	}
	dir, err := openBoard(board)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	name := "goal-" + goalID
	if err := unix.Unlinkat(dir, name, 0); err != nil && !errors.Is(err, syscall.ENOENT) {
		_ = unix.Close(dir)
		return fmt.Errorf("goalfile: unlink %s: %w", name, err)
	}
	_ = unix.Close(dir)
	left, err := boardEntries(board)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(left) != 1 || left[0] != ".gitignore" {
		return nil
	}
	dir, err = openBoard(board)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = unix.Close(dir) }()
	// Only the marker the agent wrote is unlinked: a foreign file keeps
	// the folder. Merge seam: these bytes must unify with
	// internal/agent/worktree.Marker (lane 1); both quote 01d2 exactly.
	raw, rerr := readAt(dir, ".gitignore")
	if rerr != nil || string(raw) != "# fleet-board goal files; this folder ignores itself\n*\n" {
		return nil
	}
	if err := unix.Unlinkat(dir, ".gitignore", 0); err != nil && !errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("goalfile: unlink .gitignore: %w", err)
	}
	if err := removeBoard(workdir, board); err != nil {
		return err
	}
	return nil
}

// ReadGoalFile reads goal-<goal_id> no-follow (a regular file with
// link count 1, at most 4 KiB) and parses its first and CONSENT: lines
// (01d2 step 5). A missing file is os.ErrNotExist.
func (w Writer) ReadGoalFile(workdir, goalID string) (File, error) {
	if !goalIDRule.MatchString(goalID) {
		return File{}, fmt.Errorf("goalfile: read %q: %w", goalID, ErrGoalID)
	}
	board, _, err := w.board(workdir)
	if err != nil {
		return File{}, err
	}
	dir, err := openBoard(board)
	if err != nil {
		return File{}, err
	}
	defer func() { _ = unix.Close(dir) }()
	raw, err := readAt(dir, "goal-"+goalID)
	if err != nil {
		return File{}, fmt.Errorf("goalfile: read goal-%s: %w", goalID, err)
	}
	return Parse(raw), nil
}

// board runs the injected bootstrap, or refuses when no bootstrap was
// injected (the lane-1 merge seam).
func (w Writer) board(workdir string) (string, bool, error) {
	if w.EnsureBoard == nil {
		return "", false, fmt.Errorf("%w", ErrBoardUnwired)
	}
	return w.EnsureBoard(workdir)
}

// openBoard opens the board directory no-follow and proves it is a
// directory. A symlink, a file or a missing folder is refused; a
// missing folder maps to os.ErrNotExist for the release path.
func openBoard(board string) (int, error) {
	dir, err := unix.Open(board, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ENOTDIR) {
			return -1, fmt.Errorf("goalfile: open board: %w", mapNotExist(err))
		}
		return -1, fmt.Errorf("goalfile: open board: %w", notPlain(err))
	}
	var st unix.Stat_t
	if err := unix.Fstat(dir, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		_ = unix.Close(dir)
		return -1, fmt.Errorf("goalfile: stat board: %w", ErrDirNotPlain)
	}
	return dir, nil
}

// sameContent reports whether the name through dir holds content. A
// symlink is refused (never followed, never compared).
func sameContent(dir int, name string, content []byte) (bool, error) {
	raw, err := readAt(dir, name)
	if err != nil {
		return false, err
	}
	return string(raw) == string(content), nil
}

// readAt reads a regular file with link count 1 through dir, at most
// maxReadBytes+1 to tell oversize apart.
func readAt(dir int, name string) ([]byte, error) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("%w", os.ErrNotExist)
		}
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("goalfile: %s is %w", name, ErrGoalFileUnsafe)
		}
		return nil, err
	}
	// The *os.File owns fd from here: close it, never the raw fd, or
	// its finalizer closes a reused number later.
	f := os.NewFile(uintptr(fd), name)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Nlink != 1 {
		return nil, fmt.Errorf("goalfile: %s is %w", name, ErrGoalFileUnsafe)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxReadBytes {
		return nil, fmt.Errorf("goalfile: %s is over %d bytes: %w", name, maxReadBytes, ErrGoalFileUnsafe)
	}
	return raw, nil
}

// boardEntries lists the board through a fresh guarded open.
func boardEntries(board string) ([]string, error) {
	dir, err := openBoard(board)
	if err != nil {
		return nil, err
	}
	d := os.NewFile(uintptr(dir), board)
	names, rerr := d.Readdirnames(-1)
	cerr := d.Close()
	if rerr != nil {
		return nil, fmt.Errorf("goalfile: list board: %w", rerr)
	}
	if cerr != nil {
		return nil, fmt.Errorf("goalfile: list board: %w", cerr)
	}
	return names, nil
}

// removeBoard removes an empty .fleet-board through the workdir handle:
// the board is proved to be the same directory just inspected (device
// and inode), so a swapped path is never removed.
func removeBoard(workdir, board string) error {
	wfd, err := unix.Open(workdir, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("goalfile: open workdir: %w", notPlain(err))
	}
	defer func() { _ = unix.Close(wfd) }()
	var wst unix.Stat_t
	if err := unix.Fstat(wfd, &wst); err != nil {
		return fmt.Errorf("goalfile: stat workdir: %w", err)
	}
	if wst.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return fmt.Errorf("goalfile: workdir is %w", ErrDirNotPlain)
	}
	// The board was just seen holding only the marker; prove the name
	// still points at that directory before removing it.
	const dot = ".fleet-board"
	var nst unix.Stat_t
	if err := unix.Fstatat(wfd, dot, &nst, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil
		}
		return fmt.Errorf("goalfile: stat board: %w", notPlain(err))
	}
	if nst.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return fmt.Errorf("goalfile: board is %w", ErrDirNotPlain)
	}
	dir, err := openBoard(board)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var cur unix.Stat_t
	if ferr := unix.Fstat(dir, &cur); ferr != nil {
		_ = unix.Close(dir)
		return fmt.Errorf("goalfile: stat board: %w", ferr)
	}
	_ = unix.Close(dir)
	if cur.Dev != nst.Dev || cur.Ino != nst.Ino {
		return fmt.Errorf("goalfile: board changed underfoot: %w", ErrDirNotPlain)
	}
	if err := unix.Unlinkat(wfd, dot, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, syscall.ENOENT) &&
		!errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
		return fmt.Errorf("goalfile: remove board: %w", err)
	}
	return nil
}

// writeClose writes every byte, syncs and closes fd.
func writeClose(fd int, content []byte) error {
	f := os.NewFile(uintptr(fd), "goal")
	_, werr := f.Write(content)
	serr := f.Sync()
	cerr := f.Close()
	return errors.Join(werr, serr, cerr)
}

// notPlain maps a symlink refusal to ErrDirNotPlain and passes the
// rest through.
func notPlain(err error) error {
	if errors.Is(err, syscall.ELOOP) {
		return fmt.Errorf("%w", ErrDirNotPlain)
	}
	return err
}

// mapNotExist keeps a missing folder distinguishable for the release path.
func mapNotExist(err error) error {
	if errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("%w", os.ErrNotExist)
	}
	return err
}
