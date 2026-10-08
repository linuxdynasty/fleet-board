package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// event is one control-mode notification or a finished command reply, with
// the time the probe read its last byte.
type event struct {
	T     time.Time
	Line  Line
	Reply *Reply
}

// control is a tmux -C client driven over pipes.
type control struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stderr  bytes.Buffer
	events  chan event
	paused  atomic.Bool
	done    chan struct{}
	exitErr error
	mu      sync.Mutex
}

// startControl runs "tmux -C <attachArgs...>" and waits up to 3 s for the
// first reply (the attach itself) or for the client to exit.
func startControl(s *server, attachArgs ...string) (*control, *Reply, error) {
	c := &control{events: make(chan event, 65536), done: make(chan struct{})}
	c.cmd = s.command(context.Background(), append([]string{"-C"}, attachArgs...)...)
	c.cmd.Stderr = &c.stderr
	var err error
	if c.stdin, err = c.cmd.StdinPipe(); err != nil {
		return nil, nil, err
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := c.cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start control client: %w", err)
	}
	go c.read(stdout)
	r, err := c.next(3 * time.Second)
	if err != nil {
		return c, nil, err
	}
	return c, r, nil
}

// gatedReader stops reading while paused so the pipe fills and tmux sees a
// slow control client (used to force %pause).
type gatedReader struct {
	r      io.Reader
	paused *atomic.Bool
}

func (g gatedReader) Read(p []byte) (int, error) {
	for g.paused.Load() {
		time.Sleep(10 * time.Millisecond)
	}
	if len(p) > 4096 {
		p = p[:4096]
	}
	return g.r.Read(p)
}

func (c *control) read(stdout io.Reader) {
	defer close(c.events)
	br := bufio.NewReaderSize(gatedReader{r: stdout, paused: &c.paused}, 4096)
	var b blockState
	for {
		raw, err := br.ReadString('\n')
		if raw != "" {
			raw = strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
			now := time.Now()
			l := ParseLine(raw)
			if r, in := b.feed(l, raw); r != nil {
				c.events <- event{T: now, Reply: r}
			} else if !in {
				c.events <- event{T: now, Line: l}
			}
		}
		if err != nil {
			c.exitErr = c.cmd.Wait()
			close(c.done)
			return
		}
	}
}

var errExited = errors.New("control client exited")

// next returns the next reply, dropping notifications; use collect when the
// notifications matter.
func (c *control) next(timeout time.Duration) (*Reply, error) {
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				return nil, errExited
			}
			if ev.Reply != nil {
				return ev.Reply, nil
			}
		case <-deadline:
			return nil, fmt.Errorf("no reply within %s", timeout)
		}
	}
}

// send writes one command line. Newlines are refused so one call can never
// carry a second tmux command.
func (c *control) send(line string) error {
	if strings.ContainsAny(line, "\r\n") {
		return fmt.Errorf("refusing command with a newline")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err := io.WriteString(c.stdin, line+"\n")
	return err
}

// run sends a command and returns its reply plus any notifications that
// arrived before it.
func (c *control) run(line string) (*Reply, []event, error) {
	if err := c.send(line); err != nil {
		return nil, nil, err
	}
	var notes []event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-c.events:
			if !ok {
				return nil, notes, errExited
			}
			if ev.Reply != nil {
				return ev.Reply, notes, nil
			}
			notes = append(notes, ev)
		case <-deadline:
			return nil, notes, fmt.Errorf("no reply to %q", line)
		}
	}
}

// collect returns every event until the deadline. Replies are kept too.
func (c *control) collect(until time.Time) []event {
	var out []event
	for {
		d := time.Until(until)
		if d <= 0 {
			return out
		}
		select {
		case ev, ok := <-c.events:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-time.After(d):
			return out
		}
	}
}

// close detaches by closing stdin and waits up to 3 s for the exit.
func (c *control) close() {
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		_ = c.cmd.Process.Kill()
		<-c.done
	}
}

// exited reports whether the client process has ended and its stderr.
func (c *control) exited(wait time.Duration) (bool, string) {
	select {
	case <-c.done:
		return true, strings.TrimSpace(c.stderr.String())
	case <-time.After(wait):
		return false, ""
	}
}
