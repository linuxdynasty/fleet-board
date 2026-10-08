package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
)

// typer is a normal (non-control) tmux client attached through a
// pseudo-terminal the probe owns, standing in for a person at the keyboard.
// tmux reports it in list-clients with client_pid == pid.
type typer struct {
	cmd   *exec.Cmd
	pty   *os.File
	pid   int
	bytes atomic.Int64
	done  chan struct{}
}

func startTyper(s *server, session string, cols, rows uint16) (*typer, error) {
	cmd := s.command(context.Background(), "attach-session", "-t", session)
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("attach through pty: %w", err)
	}
	t := &typer{cmd: cmd, pty: f, pid: cmd.Process.Pid, done: make(chan struct{})}
	go func() {
		// Drain the client's screen output so it never blocks; the bytes are
		// counted, not kept.
		n, _ := io.Copy(io.Discard, f)
		t.bytes.Add(n)
		_ = cmd.Wait()
		close(t.done)
	}()
	return t, nil
}

// key writes the keystroke and returns the time just before the write.
func (t *typer) key(s string) (time.Time, error) {
	t0 := time.Now()
	_, err := t.pty.WriteString(s)
	return t0, err
}

// close detaches with the default prefix (C-b d), then kills if needed.
func (t *typer) close() {
	_, _ = t.pty.WriteString("\x02d")
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
		_ = t.cmd.Process.Kill()
		<-t.done
	}
	_ = t.pty.Close()
}
