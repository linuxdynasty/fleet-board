package logship

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// Runner runs one tmux CLI call. *proc.Runner satisfies it; the seam keeps
// tests on the production path (a throwaway server) instead of a fake.
type Runner interface {
	Run(ctx context.Context, sp proc.Spec) (proc.Result, error)
}

// paneRE is the shape of a tmux pane id: %N only (ids.proto). Anything
// else is never sent to tmux, so a pane key from the hub cannot smuggle a
// second command, and the raw file name cannot escape its directory.
var paneRE = regexp.MustCompile(`^%[0-9]+$`)

// serverRE is the shape of a PaneKey server name: the wiring row passes one
// of "default", "fleet" or "fleet-probe", and the raw file layout keys on
// it, so it must be a safe path element.
var serverRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Options builds a Tracker capturing panes of one tmux server into the
// agent's raw files. The wiring row passes the server's socket and the
// agent state directory; StartGoal step 7 passes nothing else.
type Options struct {
	Runner Runner
	// Caller is proc.For("tmux"): the one-shot caps for tmux CLI calls.
	Caller proc.Caller
	// Tmux is the tmux binary; empty means "tmux" on PATH.
	Tmux string
	// Socket is the server's socket path, passed as -S. Always explicit:
	// there is no default server.
	Socket string
	// Server names the tmux server for the raw file layout.
	Server string
	// Dir is the agent state directory; raw files live under its raw/.
	Dir string
}

// Tracker captures goal panes with tmux pipe-pane into plain append files
// (K1). One goroutine owns each Tracker call; tmux serializes the pipe
// changes itself.
type Tracker struct {
	runner Runner
	caller proc.Caller
	tmux   string
	socket string
	server string
	dir    string

	mu     sync.Mutex
	active map[string]bool
}

// New validates the Tracker's shape and returns it. Nothing touches tmux
// or the filesystem here: Start creates the raw directory on first use.
func New(o Options) (*Tracker, error) {
	if o.Runner == nil {
		return nil, fmt.Errorf("logship: Runner is required (*proc.Runner satisfies it)")
	}
	if o.Caller.Name() == "" {
		return nil, fmt.Errorf("logship: Caller is required (proc.For(\"tmux\"))")
	}
	if o.Socket == "" {
		return nil, fmt.Errorf("logship: Socket is required (-S, always explicit)")
	}
	if !serverRE.MatchString(o.Server) {
		return nil, fmt.Errorf("logship: Server %q is not a server name", o.Server)
	}
	if o.Dir == "" {
		return nil, fmt.Errorf("logship: Dir is required (the agent state directory)")
	}
	tmux := o.Tmux
	if tmux == "" {
		tmux = "tmux"
	}
	return &Tracker{
		runner: o.Runner,
		caller: o.Caller,
		tmux:   tmux,
		socket: o.Socket,
		server: o.Server,
		dir:    filepath.Clean(o.Dir),
		active: map[string]bool{},
	}, nil
}

// Start begins pipe-pane -o append capture of pane into its raw file and
// returns the file's path. Declarative: starting an active capture runs
// pipe-pane again onto the same file, so a repeated start never fails and
// a restart appends where the last run left off.
func (t *Tracker) Start(ctx context.Context, pane string) (string, error) {
	if !paneRE.MatchString(pane) {
		return "", fmt.Errorf("logship: pane %q is not a pane id", pane)
	}
	path := t.rawPath(pane)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("logship: make raw dir: %w", err)
	}
	// Pre-create the raw file mode 0600: pane output can hold secrets,
	// and the shell redirection below would otherwise follow the umask.
	//nolint:gosec // G302, G306: 0600 is the point; the path is agent-built, never from the hub
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("logship: create raw file: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("logship: create raw file: %w", err)
	}
	// One argv element past -t: the whole pipe command is data to tmux,
	// never re-split. tmux expands status-left formats in it, so the path
	// is tmux-escaped first (tmuxEscape), then single-quoted for the shell
	// (quote): a state dir with spaces, quotes, `%` or `#` cannot break
	// either line.
	_, err = t.runner.Run(ctx, proc.Spec{
		Caller: t.caller,
		Path:   t.tmux,
		Args:   []string{"-S", t.socket, "pipe-pane", "-o", "-t", pane, "cat >> " + quote(tmuxEscape(path))},
	})
	if err != nil {
		return "", fmt.Errorf("logship: pipe-pane %s: %w", pane, err)
	}
	t.mu.Lock()
	t.active[pane] = true
	t.mu.Unlock()
	return path, nil
}

// Stop ends pane's capture. Declarative: stopping a capture that was never
// started is OK, so a repeated stop never fails.
func (t *Tracker) Stop(ctx context.Context, pane string) error {
	if !paneRE.MatchString(pane) {
		return fmt.Errorf("logship: pane %q is not a pane id", pane)
	}
	t.mu.Lock()
	active := t.active[pane]
	t.mu.Unlock()
	if !active {
		return nil
	}
	// No shell command: tmux then closes the pane's pipe.
	_, err := t.runner.Run(ctx, proc.Spec{
		Caller: t.caller,
		Path:   t.tmux,
		Args:   []string{"-S", t.socket, "pipe-pane", "-t", pane},
	})
	if err != nil {
		return fmt.Errorf("logship: pipe-pane stop %s: %w", pane, err)
	}
	t.mu.Lock()
	delete(t.active, pane)
	t.mu.Unlock()
	return nil
}

// Captured reports whether pane's capture is on in this process: the read
// seam for the future TmuxPane.log_capture level (row 25 wires it into the
// inventory). A server restart can drop a pipe the map still names; the
// next Start re-points it.
func (t *Tracker) Captured(pane string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active[pane]
}

// Active lists every pane captured in this process, sorted, for the future
// snapshot level and for tests.
func (t *Tracker) Active() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.active))
	for pane := range t.active {
		out = append(out, pane)
	}
	sort.Strings(out)
	return out
}

// rawPath is pane's plain append file: <dir>/raw/<server>/<digits>.log.
// The pane id loses its `%`: tmux expands `%`-sequences in the pipe
// command, so the raw name carries digits only (`%3` maps to `3.log`); the
// server directory keeps panes of different servers apart. Every element is
// agent-built from charset-checked fields, so no hub text can reach the
// filesystem.
func (t *Tracker) rawPath(pane string) string {
	return filepath.Join(t.dir, "raw", t.server, strings.TrimPrefix(pane, "%")+".log")
}

// tmuxEscape escapes a literal path for tmux's status-left format
// expansion in the pipe-pane command: `%` to `%%`, `#` to `##` (a lone `#`
// starts a format like `#W` or `#{...}`). Both are documented format
// semantics, verified live against tmux 3.8; the CI matrix covers the
// oldest supported release (3.2a) too.
func tmuxEscape(path string) string {
	path = strings.ReplaceAll(path, "%", "%%")
	return strings.ReplaceAll(path, "#", "##")
}

// quote single-quotes one shell word, so a path with spaces, quotes or
// other metacharacters stays one word to the pipe shell. It runs after
// tmuxEscape: the `'\”` sequence it introduces holds no `%` or `#`.
func quote(word string) string {
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}
