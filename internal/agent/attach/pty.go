package attach

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Client is one PTY-backed `tmux attach` child: the browser's eyes on a
// grouped view session. Master is the agent's handle; the child holds the
// slave and dies when the master closes. Wait reaps it; Close hangs up and
// reaps, and is safe to call twice.
type Client struct {
	// mu guards Master and cmd: the pumps read them while Close
	// nils them, so every access goes through master or under mu.
	mu     sync.Mutex
	Master *os.File
	cmd    *exec.Cmd
}

// master returns the PTY master, or nil once Close ran.
func (c *Client) master() *os.File {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Master
}

// OpenClient opens a PTY sized cols×rows and starts
// `program -S socket attach-session -t view` on it. The size is set before
// the child starts, so the client's first render already has the browser's
// dimensions. readOnly adds `-f read-only`: tmux itself then ignores keys
// typed in the client (only detach/switch-client bindings work), a third
// layer under the hub's and the pump's input drops. It is `-f read-only`
// and not `-r`: `-r` also implies ignore-size, which would unhook the
// client from the session's size and break resize. Agent CLI selects
// (select-window/pane -t view) are server-side and unaffected.
func OpenClient(program, socket, view string, cols, rows int, readOnly bool) (*Client, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("attach: open pty for %q: %w", view, err)
	}
	// Best-effort cleanup: every error below closes both ends.
	defer func() {
		if err != nil {
			_ = master.Close()
			_ = slave.Close()
		}
	}()
	// Cells fit: validDims checked both above.
	rows16 := uint16(rows) //nolint:gosec // G115: as above
	cols16 := uint16(cols) //nolint:gosec // G115: as above
	if err = pty.Setsize(master, &pty.Winsize{Rows: rows16, Cols: cols16}); err != nil {
		return nil, fmt.Errorf("attach: size pty for %q: %w", view, err)
	}
	if !validDims(cols, rows) {
		return nil, fmt.Errorf("attach: cols %d and rows %d name a terminal", cols, rows)
	}
	args := []string{"-S", socket, "attach-session", "-t", view}
	if readOnly {
		args = []string{"-S", socket, "attach-session", "-f", "read-only", "-t", view}
	}
	// The program is the configured tmux binary and every argument is a
	// typed flag: never a shell string. The client outlives the command
	// that opened it, so no context may kill it; Manager.Close owns it.
	cmd := exec.Command(program, args...) //nolint:gosec,noctx // G204: as above; noctx: as above
	cmd.Env = clientEnv()
	// The child inherits the agent's environment on the slave: a real
	// tmux client sees the user's terminal the same way.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("attach: start tmux attach for %q: %w", view, err)
	}
	// The child has its copy of the slave; the parent keeps the master.
	_ = slave.Close()
	return &Client{Master: master, cmd: cmd}, nil
}

// clientEnv is the attach client's environment: the agent's, with a TERM
// the client can talk to. Without a capable TERM the client dies at once
// ("open terminal failed") and the open burns the whole land wait; agents
// run without one (launchd, systemd, CI), so default it instead of
// inheriting the failure. A capable inherited TERM is kept. Nil keeps
// os.Environ as is.
func clientEnv() []string {
	if term, ok := os.LookupEnv("TERM"); ok && term != "" && term != "dumb" && term != "unknown" {
		return nil
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TERM=") {
			env = append(env, kv)
		}
	}
	return append(env, "TERM=xterm-256color")
}

// killGrace is how long a HUP'd client gets to detach before SIGKILL.
const killGrace = 2 * time.Second

// Close hangs the client up and reaps it: HUP first so tmux detaches
// cleanly, KILL past the grace, then Wait. The signal comes first because
// closing the master alone does not end the client: the out pump's Read
// holds a Go reference on this blocking file, so Close reports success
// without closing the fd (internal/poll), and with no hangup the reader
// would sleep forever and Wait with it. Killing the client delivers EIO
// to the reader, whose exit drops the last reference and really closes
// the fd. A client that already exited is reaped, never left a zombie.
// Wait errors are ignored: the view teardown (ArmDestroy/KillView) owns
// the authoritative cleanup, and a dead client is the expected state,
// not a failure to report.
func (c *Client) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.Master != nil {
		_ = c.Master.Close()
		c.Master = nil
	}
	cmd := c.cmd
	c.cmd = nil
	c.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGHUP)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(killGrace):
		_ = cmd.Process.Kill()
		<-done
	}
}

// Resize sets the client's terminal size in cells.
func (c *Client) Resize(cols, rows int) error {
	master := c.master()
	if master == nil {
		return fmt.Errorf("attach: resize a closed client")
	}
	if !validDims(cols, rows) {
		return fmt.Errorf("attach: cols %d and rows %d name a terminal", cols, rows)
	}
	// Cells fit: validDims bounds both to 1..1000.
	rows16 := uint16(rows) //nolint:gosec // G115: as above
	cols16 := uint16(cols) //nolint:gosec // G115: as above
	return pty.Setsize(master, &pty.Winsize{Rows: rows16, Cols: cols16})
}
