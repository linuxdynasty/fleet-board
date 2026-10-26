package hooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// InstallError is a G1 install, verify or uninstall failure with the
// contract reason. Unparseable user files and shape mismatches are
// HOOKS_INSTALL_FAILED; the file is left untouched. An unknown harness is
// INVALID_ARGUMENT: the agent never guesses a config layout.
type InstallError struct {
	Reason typev1.ReasonCode
	Op     string
	Path   string
	Err    error
}

func (e *InstallError) Error() string {
	return fmt.Sprintf("hooks: %s %s: %v", e.Op, e.Path, e.Err)
}

func (e *InstallError) Unwrap() error { return e.Err }

// ReasonOf maps any error from this package to its contract reason.
func ReasonOf(err error) typev1.ReasonCode {
	var ie *InstallError
	if ok := errors.As(err, &ie); ok {
		return ie.Reason
	}
	return typev1.ReasonCode_REASON_CODE_HOOKS_INSTALL_FAILED
}

func installFailed(op, path string, err error) *InstallError {
	return &InstallError{Reason: typev1.ReasonCode_REASON_CODE_HOOKS_INSTALL_FAILED, Op: op, Path: path, Err: err}
}

func invalidArg(op, path string, err error) *InstallError {
	return &InstallError{Reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, Op: op, Path: path, Err: err}
}

// backupFile copies raw next to path without touching the original.
func backupFile(path string, raw []byte) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	name := path + ".fleet-backup." + stamp
	for i := 2; ; i++ {
		if _, err := os.Stat(name); os.IsNotExist(err) {
			break
		}
		name = fmt.Sprintf("%s.fleet-backup.%s-%d", path, stamp, i)
	}
	if err := os.WriteFile(name, raw, 0o600); err != nil { //nolint:gosec // G703: the name is the config path plus a fixed backup suffix
		return "", err
	}
	return name, nil
}

// writeAtomic writes data to path through a temp file in the same
// directory plus rename, so a crash never leaves a half-written config.
// Missing parent directories are created, so a fresh install needs none.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: the user config and data parents are shared, ordinary folders
		return err
	}
	tmp, err := os.CreateTemp(dir, ".fleet-hooks-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
