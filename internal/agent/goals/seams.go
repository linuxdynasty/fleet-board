package goals

import (
	"context"
	"os"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

// rescanInterval is the goal-file rescan (02h "Observed goals"): the
// agent re-reads every pane's checkout this often, outside the usage and
// GitHub polls (ADR 0022 A.6 allows this timed check and the tmux poll).
const rescanInterval = 60 * time.Second

// RescanInterval is how often the caller runs [Tracker.Scan].
func RescanInterval() time.Duration { return rescanInterval }

// Pane is one live inventory pane the tracker may attribute a goal to.
type Pane struct {
	Key     *typev1.PaneKey
	Session *typev1.SessionId
	Server  string // socket name for detail queries
	ID      string // tmux pane id ("%N") for detail queries
	Dead    bool
}

// Panes lists candidate panes and locates them in tmux. Implementations
// read the observer's inventory and query the control clients; tests stub
// both.
type Panes interface {
	// List returns the live panes (internal servers excluded).
	List(ctx context.Context) ([]Pane, error)
	// Locate returns the pane's working folder and pid. A dead or vanished
	// pane is an error: the caller skips it this tick.
	Locate(ctx context.Context, server, id string) (path string, pid int32, err error)
}

// Files reads goal files. OSFiles does; tests use real checkouts on disk,
// so this seam exists only to keep the tracker off direct I/O.
type Files interface {
	Select(dir string, after time.Time) (path string, mod time.Time, ok bool, err error)
	Read(path string) ([]byte, error)
}

// OSFiles reads real goal files.
type OSFiles struct{}

// Select returns the newest eshu goal file newer than after.
func (OSFiles) Select(dir string, after time.Time) (string, time.Time, bool, error) {
	return goalfile.Select(dir, after)
}

// Read reads a goal file whole (files are small; the parser caps lines).
func (OSFiles) Read(path string) ([]byte, error) { return os.ReadFile(path) } //nolint:gosec // G304: the tracker's own selected file
