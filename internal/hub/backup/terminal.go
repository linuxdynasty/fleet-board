package backup

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// maxPassphrase bounds one passphrase line.
const maxPassphrase = 1024

// NewTerminal is the real [Terminal]: in is a terminal only when the termios
// ioctl succeeds on it, so a pipe, a file or /dev/null is refused. Prompts go
// to out (stderr), and the line is read with echo off.
func NewTerminal(in *os.File, out io.Writer) Terminal {
	return ttyTerminal{in: in, out: out}
}

type ttyTerminal struct {
	in  *os.File
	out io.Writer
}

func (t ttyTerminal) fd() int { return int(t.in.Fd()) }

func (t ttyTerminal) IsTerminal() bool {
	_, err := unix.IoctlGetTermios(t.fd(), ioctlReadTermios)
	return err == nil
}

// ReadPassword turns echo off for one line and restores the old settings
// afterwards.
func (t ttyTerminal) ReadPassword(prompt string) (line []byte, err error) {
	old, err := unix.IoctlGetTermios(t.fd(), ioctlReadTermios)
	if err != nil {
		return nil, ErrNotTerminal
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	quiet.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(t.fd(), ioctlWriteTermios, &quiet); err != nil {
		return nil, fmt.Errorf("backup: turn echo off: %w", err)
	}
	defer func() {
		if rerr := unix.IoctlSetTermios(t.fd(), ioctlWriteTermios, old); rerr != nil && err == nil {
			clear(line)
			line, err = nil, fmt.Errorf("backup: restore the terminal: %w", rerr)
		}
	}()
	if _, err := io.WriteString(t.out, prompt); err != nil {
		return nil, err
	}
	line, err = readLine(t.in)
	_, _ = io.WriteString(t.out, "\n") // the newline echo swallowed
	return line, err
}

// readLine reads one byte at a time up to a newline, so nothing past the
// line is consumed; a trailing carriage return is dropped.
func readLine(r io.Reader) ([]byte, error) {
	var buf [1]byte
	var line []byte
	for {
		n, err := r.Read(buf[:])
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			if len(line) == maxPassphrase {
				clear(line)
				return nil, fmt.Errorf("backup: the passphrase is longer than %d bytes", maxPassphrase)
			}
			line = append(line, buf[0])
		}
		if errors.Is(err, io.EOF) && len(line) > 0 {
			break
		}
		if err != nil {
			clear(line)
			return nil, err
		}
	}
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	return line, nil
}
