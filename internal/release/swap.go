package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// FileSwapper installs one verified archive beside the running binary. The
// archive holds exactly one file, named for the program; anything else is
// refused and the old binary stays untouched.
type FileSwapper struct {
	// Dir is the folder holding the binary. Empty means the folder of the
	// running binary itself.
	Dir string
}

// Swap extracts the archive and replaces the installed binary in one rename,
// so there is never a half-written binary. The file lands mode 0755.
func (s FileSwapper) Swap(archive []byte, program string) error {
	if program != ProgramFleetd && program != ProgramAgent && program != ProgramFleet {
		return fmt.Errorf("release: no install for %q", program)
	}
	body, err := extractOne(archive, program)
	if err != nil {
		return err
	}
	dir := s.Dir
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("release: find the running binary: %w", err)
		}
		dir = filepath.Dir(exe)
	}
	dest := filepath.Join(dir, program)
	tmp, err := os.CreateTemp(dir, program+".new-*")
	if err != nil {
		return fmt.Errorf("release: stage the new binary: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("release: stage the new binary: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("release: stage the new binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("release: stage the new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("release: stage the new binary: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("release: replace the binary: %w", err)
	}
	if d, err := os.Open(dir); err == nil { //nolint:gosec // G304: same dir as above
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// extractOne reads the single binary out of a release archive. The archive
// must hold exactly one regular file, named for the program with no folder.
func extractOne(archive []byte, program string) ([]byte, error) {
	if len(archive) == 0 {
		return nil, fmt.Errorf("release: the archive is empty")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("release: the archive is not a gzip file")
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var body []byte
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("release: bad archive: %w", err)
		}
		name := filepath.ToSlash(hdr.Name)
		if !hdr.FileInfo().Mode().IsRegular() {
			return nil, fmt.Errorf("release: the archive holds %q, not a plain file", shortLine(name))
		}
		if body != nil {
			return nil, fmt.Errorf("release: the archive holds more than one file")
		}
		if name != program || strings.Contains(name, "/") {
			return nil, fmt.Errorf("release: the archive holds %q, want one file named %q", shortLine(name), program)
		}
		if hdr.Size < 0 || hdr.Size > maxDownload {
			return nil, fmt.Errorf("release: the archive's file size is not sane")
		}
		body, err = io.ReadAll(io.LimitReader(tr, maxDownload+1))
		if err != nil {
			return nil, fmt.Errorf("release: read the archive: %w", err)
		}
	}
	if body == nil {
		return nil, fmt.Errorf("release: the archive holds no file")
	}
	return body, nil
}

// ServiceRestarter restarts the installed unit after the swap, through the
// platform's service manager: launchctl on macOS, systemctl --user on Linux.
type ServiceRestarter struct {
	// GOOS defaults to the running platform.
	GOOS string
	// Run starts one child; nil runs through internal/proc (bounded, 01
	// principle 8). Tests pass a fake.
	Run func(ctx context.Context, path string, args ...string) error
}

// Restart restarts the program's unit. A failing restart is an error naming
// the unit; the new binary is already installed, and starting it by hand
// (or the next login) picks it up.
func (r ServiceRestarter) Restart(program string) error {
	if program != ProgramFleetd && program != ProgramAgent {
		return fmt.Errorf("release: no service for %q", program)
	}
	goos := r.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	var path string
	var args []string
	switch goos {
	case "darwin":
		path = "launchctl"
		args = []string{"kickstart", "-k", "gui/" + strconv.Itoa(os.Getuid()) + "/com.fleet-board." + program}
	case "linux":
		path = "systemctl"
		args = []string{"--user", "restart", program + ".service"}
	default:
		return fmt.Errorf("release: no service restart on %s", goos)
	}
	run := r.Run
	if run == nil {
		run = procRun
	}
	if err := run(context.Background(), path, args...); err != nil {
		return fmt.Errorf("release: restart the service: %w", err)
	}
	return nil
}

func procRun(ctx context.Context, path string, args ...string) error {
	caller, err := proc.For("service")
	if err != nil {
		return err
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		return err
	}
	res, err := runner.Run(ctx, proc.Spec{Caller: caller, Path: path, Args: args})
	out := strings.TrimSpace(string(res.Stderr) + string(res.Stdout))
	if err != nil {
		if out == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, out)
	}
	if res.ExitCode != 0 {
		if out == "" {
			return fmt.Errorf("exit %d", res.ExitCode)
		}
		return fmt.Errorf("exit %d: %s", res.ExitCode, out)
	}
	return nil
}
