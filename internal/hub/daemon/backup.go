package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
)

// BackupRequest is what `fleetd backup` asks for.
type BackupRequest struct {
	// Encrypt wraps the copy with age under a passphrase read from
	// Options.Terminal.
	Encrypt bool
}

// Backup is `fleetd backup [--encrypt]`: it reads fleetd.yaml for the state
// folder and writes one manual copy of state.db into backups/
// (backup.Take). It takes no lock, neither fleetd.lock nor any other, so it
// works whether or not fleetd run is up. Its log lines (backup.completed,
// backup.failed) go to the command's own telemetry.
func Backup(ctx context.Context, opts Options, req BackupRequest) (res backup.Result, err error) {
	if req.Encrypt && opts.Terminal == nil {
		return res, errors.New("daemon: --encrypt needs a terminal to read the passphrase from")
	}
	cfg, err := config.Load(opts.ConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("%s does not exist; run fleetd init first: %w", opts.ConfigPath, err)
	}
	if err != nil {
		return res, err
	}
	s, err := newSession("backup", opts)
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, s.close()) }()
	o := backup.Options{StateDir: cfg.StateDir, Trigger: backup.Manual, Now: opts.Now, Telemetry: s.tel}
	if req.Encrypt {
		o.Terminal = opts.Terminal
	}
	return backup.Take(ctx, o)
}
