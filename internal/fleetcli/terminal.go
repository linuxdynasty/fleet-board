package fleetcli

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

var (
	errNotTerminal   = errors.New("terminal required")
	errInvalidSecret = errors.New("invalid approval secret")
)

func isTerminal(in *os.File) bool {
	if in == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(in.Fd()), ioctlReadTermios)
	return err == nil
}

// readSecret owns the terminal settings on its caller's goroutine. A short
// poll lets context cancellation restore echo without waiting for Enter.
func readSecret(ctx context.Context, in *os.File, prompt io.Writer) (line []byte, err error) {
	if in == nil {
		return nil, errNotTerminal
	}
	fd := int(in.Fd())
	old, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return nil, errNotTerminal
	}
	quiet := *old
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	quiet.Iflag |= unix.ICRNL
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &quiet); err != nil {
		return nil, errNotTerminal
	}
	defer func() {
		if restoreErr := unix.IoctlSetTermios(fd, ioctlFlushTermios, old); restoreErr != nil {
			clear(line)
			line, err = nil, errors.New("restore terminal failed")
		}
	}()
	if _, err := io.WriteString(prompt, "Local approval secret: "); err != nil {
		return nil, err
	}
	line = make([]byte, 0, 64)
	invalid := false
	for {
		if err := ctx.Err(); err != nil {
			clear(line)
			return nil, err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, pollErr := unix.Poll(fds, 50)
		if pollErr == unix.EINTR {
			continue
		}
		if pollErr != nil {
			clear(line)
			return nil, errors.New("terminal read failed")
		}
		if n == 0 {
			continue
		}
		var one [1]byte
		count, readErr := unix.Read(fd, one[:])
		if readErr == unix.EINTR {
			continue
		}
		if readErr != nil || count != 1 {
			clear(line)
			return nil, errors.New("terminal read failed")
		}
		if one[0] == '\n' || one[0] == '\r' {
			_, _ = io.WriteString(prompt, "\n")
			if invalid || len(line) != 64 {
				clear(line)
				return nil, errInvalidSecret
			}
			return line, nil
		}
		if len(line) == 64 || !isHex(one[0]) {
			invalid = true
		} else if !invalid {
			line = append(line, one[0])
		}
	}
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }
