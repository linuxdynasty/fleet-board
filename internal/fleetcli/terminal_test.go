package fleetcli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

type signalWriter struct {
	bytes.Buffer
	ready chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "Local approval secret:") {
		select {
		case w.ready <- struct{}{}:
		default:
		}
	}
	return w.Buffer.Write(p)
}

func (w *signalWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func TestTerminalSecretDoesNotEchoAndRestoresOnCancel(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "entered", true: "canceled"}[cancel], func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = master.Close(); _ = slave.Close() }()
			old, err := unix.IoctlGetTermios(int(slave.Fd()), ioctlReadTermios)
			if err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			prompt := &signalWriter{ready: make(chan struct{}, 1)}
			done := make(chan error, 1)
			go func() {
				secret, readErr := readSecret(ctx, slave, prompt)
				if !cancel && readErr == nil && string(secret) != strings.Repeat("a", 64) {
					readErr = errors.New("read a different secret")
				}
				clear(secret)
				done <- readErr
			}()
			select {
			case <-prompt.ready:
			case err := <-done:
				t.Fatalf("read ended before prompt: %v", err)
			case <-time.After(3 * time.Second):
				t.Fatal("no prompt")
			}
			quiet, err := unix.IoctlGetTermios(int(slave.Fd()), ioctlReadTermios)
			if err != nil {
				t.Fatal(err)
			}
			if quiet.Lflag&unix.ECHO != 0 {
				t.Fatal("echo stayed on while reading")
			}
			if cancel {
				stop()
			} else if _, err := master.Write([]byte(strings.Repeat("a", 64) + "\n")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if cancel && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel = %v", err)
				}
				if !cancel && err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("read did not stop promptly")
			}
			after, err := unix.IoctlGetTermios(int(slave.Fd()), ioctlReadTermios)
			if err != nil {
				t.Fatal(err)
			}
			if after.Lflag != old.Lflag {
				t.Fatalf("termios Lflag = %d, want %d", after.Lflag, old.Lflag)
			}
		})
	}
}
