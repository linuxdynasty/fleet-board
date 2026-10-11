//go:build unix

package doctor

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// maxRead caps a file read from the state folder, as conn does.
const maxRead = 64 << 10

// pathInfo is what Lstat says about a path. A link is not followed: the
// agent refuses a state folder that is a link.
type pathInfo struct {
	Mode    fs.FileMode
	UID     int
	IsDir   bool
	Regular bool
}

func (p pathInfo) perm() fs.FileMode { return p.Mode.Perm() }

// lstat looks at path without following a link. A missing path is an
// [fs.ErrNotExist] error.
func lstat(path string) (pathInfo, error) { return info(os.Lstat(path)) }

// stat is lstat that follows a link.
func stat(path string) (pathInfo, error) { return info(os.Stat(path)) }

func info(fi fs.FileInfo, err error) (pathInfo, error) {
	if err != nil {
		return pathInfo{}, err
	}
	p := pathInfo{Mode: fi.Mode(), IsDir: fi.IsDir(), Regular: fi.Mode().IsRegular(), UID: -1}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		p.UID = int(st.Uid)
	}
	return p, nil
}

// owned reports whether the path belongs to the user running doctor.
func (e *Env) owned(p pathInfo) bool { return p.UID == e.opts.Getuid() }

func modeText(p pathInfo) string { return fmt.Sprintf("%04o", p.perm()) }

// readSmall reads a regular file of at most maxRead bytes from the state
// folder: Lstat first, then open, then a limited read.
func readSmall(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // G304: a fixed file name inside the agent's own state folder
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errNotRegular
	}
	data, err := io.ReadAll(io.LimitReader(f, maxRead+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRead {
		return nil, errTooBig
	}
	return data, nil
}

var (
	errNotRegular = errors.New("not a regular file")
	errTooBig     = errors.New("file is too big")
)
