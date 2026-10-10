package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// This is the one file in the agent that starts the long-lived
// `tmux -C attach-session` process (the depguard rule allows os/exec in
// this package's own files only; one-shot tmux calls go through
// internal/proc).

// Exit reasons: the closed `reason` of tmux.exit (05b).
const (
	reasonExit         = "exit"          // tmux wrote %exit (its session or the server ended)
	reasonEOF          = "eof"           // the stream ended without %exit (the server was killed)
	reasonDesync       = "desync"        // a reply arrived for no command: the stream lost its place
	reasonAttachFailed = "attach_failed" // a step before up failed (err_kind says which)
	reasonWriteStalled = "write_stalled" // tmux stopped reading: a command write passed writeTimeout
	reasonTooOld       = "too_old"       // tmux older than 3.2, or the flags missing from client_flags
	reasonAbsent       = "absent"        // no server answers on the socket
	reasonCancelled    = "cancelled"     // the agent stopped the client
)

// killWait is how long close waits for tmux to exit after its stdin closes
// before it kills the process.
const killWait = 2 * time.Second

// result is what a command waits for: its reply, or why none came.
type result struct {
	reply Reply
	err   error
}

// pending is one command written and not yet answered. continuePane lets
// the reader honour a %continue only in the reply to the client's own
// refresh-client -A for that pane.
type pending struct {
	continuePane PaneID // the pane of a refresh-client -A continue; NoID otherwise
	ch           chan result
}

// conn is one control client process. Lock order: wmu, then pmu; neither is
// held while calling out (Notify, logs) or while taking Client.mu.
type conn struct {
	cmd          *exec.Cmd
	writeTimeout time.Duration      // bound on one command write
	cancel       context.CancelFunc // kills the process (exec.CommandContext)
	stdin        io.WriteCloser
	parser       *Parser

	wmu sync.Mutex // serializes a command's queue entry and its write

	pmu     sync.Mutex
	pending []*pending
	paused  map[PaneID]bool
	closed  bool   // the reader is gone; no new command is queued
	reason  string // why the stream ended (an exit reason)
	exitMsg bool   // %exit was read

	done   chan struct{} // closed when the reader has returned
	repick chan struct{} // %sessions-changed tokens for repickLoop (buffered 1)
}

// startConn starts `tmux -S socket -C attach-session -E -t $N -f flags`.
// -E stops tmux applying update-environment from this client to the
// developer's session (it would replace or remove SSH_AUTH_SOCK, DISPLAY and
// the rest there). The child gets the agent's own environment without TMUX (so tmux does not
// refuse the attach as nested); its stderr is discarded (it can name the
// socket path or a session: never logged).
func startConn(ctx context.Context, program, socket string, session SessionID, flags string, opts Options, tee io.Writer) (*conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	//nolint:gosec // G204: program is the tmux the agent's facts found; the rest are fixed words, the socket the agent named and a "$N" id
	cmd := exec.CommandContext(ctx, program, "-S", socket, "-C", "attach-session", "-E", "-t", session.String(), "-f", flags)
	cmd.Env = withoutTMUX(os.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("tmux attach: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("tmux attach: %w", err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("tmux attach: %w", err)
	}
	var out io.Reader = stdout
	if tee != nil { // a test records the raw stream
		out = io.TeeReader(stdout, tee)
	}
	return &conn{
		cmd:    cmd,
		cancel: cancel,
		stdin:  stdin,
		parser: NewParser(out, opts),
		paused: map[PaneID]bool{},
		done:   make(chan struct{}),
		repick: make(chan struct{}, 1),
	}, nil
}

func withoutTMUX(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "TMUX=") {
			out = append(out, kv)
		}
	}
	return out
}

// errConnClosed is returned for a command on a conn whose reader is gone.
var errConnClosed = errors.New("tmux: control client closed")

// do writes one command line and waits for its reply. The queue entry and
// the write happen under wmu, so replies, which tmux writes in the order it
// read the commands, match the queue in order.
func (c *conn) do(ctx context.Context, line string, continuePane PaneID) (Reply, error) {
	p := &pending{continuePane: continuePane, ch: make(chan result, 1)}
	c.wmu.Lock()
	c.pmu.Lock()
	if c.closed {
		c.pmu.Unlock()
		c.wmu.Unlock()
		return Reply{}, errConnClosed
	}
	c.pending = append(c.pending, p)
	c.pmu.Unlock()
	err := c.write(ctx, line)
	c.wmu.Unlock()
	if err != nil {
		c.kill()
		return Reply{}, err
	}
	select {
	case r := <-p.ch:
		return r.reply, r.err
	case <-ctx.Done():
		// The entry stays queued so later replies still match in order.
		return Reply{}, ctx.Err()
	}
}

// write writes one command line, bounded: when tmux stops reading its input
// (or ctx ends) while the write is blocked, the conn is killed, which ends
// the write, and the attach ends with write_stalled. Called under wmu.
func (c *conn) write(ctx context.Context, line string) error {
	done := make(chan error, 1)
	go func() {
		_, err := io.WriteString(c.stdin, line+"\n")
		done <- err
	}()
	t := time.NewTimer(c.writeTimeout)
	defer t.Stop()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("tmux: write command: %w", errConnClosed)
		}
		return nil
	case <-t.C:
	case <-ctx.Done():
	}
	c.pmu.Lock()
	if c.reason == "" {
		c.reason = reasonWriteStalled
	}
	c.pmu.Unlock()
	c.kill()
	<-done // the kill closed the pipe: the write returns
	return fmt.Errorf("tmux: write command stalled: %w", errConnClosed)
}

// read is the reader goroutine: it owns the parser and stdout, matches
// replies to the queue, keeps the paused set, and hands every other message
// to notify. It returns when the stream ends; then every waiting command
// gets errConnClosed.
func (c *conn) read(notify func(Message)) {
	defer close(c.done)
	for {
		m, err := c.parser.Next()
		if err != nil {
			c.finish(reasonEOF)
			return
		}
		switch m := m.(type) {
		case Reply:
			if !c.answer(m) {
				c.finish(reasonDesync)
				c.kill()
				return
			}
			continue
		case Pause:
			c.setPaused(m.Pane, true)
		case Exit:
			c.pmu.Lock()
			c.exitMsg = true
			c.pmu.Unlock()
		}
		if notify != nil {
			notify(m)
		}
	}
}

// answer gives a reply to the oldest command. A reply tmux wrote for a
// command this client did not send (flags 0: the attach itself) answers
// nothing; one from this client with no command waiting is a desync.
func (c *conn) answer(r Reply) bool {
	if r.Flags&1 == 0 {
		return true
	}
	c.pmu.Lock()
	if len(c.pending) == 0 {
		c.pmu.Unlock()
		return false
	}
	p := c.pending[0]
	c.pending[0] = nil
	c.pending = c.pending[1:]
	for _, n := range r.ControlNotes() {
		switch n := n.(type) {
		case Pause: // pausing is the safe direction: from any reply
			c.paused[n.Pane] = true
		case Continue: // only from our own refresh-client -A for that pane
			if p.continuePane == n.Pane {
				delete(c.paused, n.Pane)
			}
		}
	}
	c.pmu.Unlock()
	p.ch <- result{reply: r}
	return true
}

func (c *conn) setPaused(pane PaneID, v bool) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	if v {
		c.paused[pane] = true
	} else {
		delete(c.paused, pane)
	}
}

func (c *conn) isPaused(pane PaneID) bool {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	return c.paused[pane]
}

// finish marks the conn closed and fails every waiting command.
func (c *conn) finish(reason string) {
	c.pmu.Lock()
	if c.reason == "" {
		c.reason = reason
		if reason == reasonEOF && c.exitMsg {
			c.reason = reasonExit
		}
	}
	c.closed = true
	waiting := c.pending
	c.pending = nil
	c.pmu.Unlock()
	for _, p := range waiting {
		p.ch <- result{err: errConnClosed}
	}
}

// why returns the reason the stream ended.
func (c *conn) why() string {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	return c.reason
}

// kill ends the process at once; the reader then sees the end of the stream.
func (c *conn) kill() { c.cancel() }

// close ends the client: stdin closed (tmux detaches the control client
// when its input ends), a kill if tmux has not exited within killWait, then
// the reader and the process are waited for.
func (c *conn) close(reason string) {
	c.pmu.Lock()
	if c.reason == "" {
		c.reason = reason
	}
	c.pmu.Unlock()
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(killWait):
		c.kill()
		<-c.done
	}
	_ = c.cmd.Wait()
	c.cancel()
}
