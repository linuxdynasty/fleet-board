package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/hub/restore"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// RestoreRequest is what `fleetd restore` asks for.
type RestoreRequest struct {
	// Backup is the backup file.
	Backup string
	// Address, when set, is added to hub_addresses and to the server
	// certificate (host or host:port; the port defaults to listen.agent's).
	Address string
}

// Restore is `fleetd restore <backup> [--address A]`, an offline command:
// it reads fleetd.yaml, creates the state folder if missing (a hub moved to
// a new host), takes fleetd.lock and holds it to the end, so it is refused
// while fleetd run holds it (#92: lock_held), removes a crashed init's or
// restore's leftovers, and replaces state.db with the backup
// ([restore.Run]), with the hub.restore audit row by the local OS user
// (api.LocalUser, kind LOCAL_USER, as on the admin socket). The server
// certificate --address needs is issued for the same name list fleetd run
// wants. A refusal before the restore starts is
// a *[StartError] logged as hub.start_failed with command restore.
func Restore(ctx context.Context, opts Options, req RestoreRequest) (res restore.Result, err error) {
	s, err := newSession("restore", opts)
	if err != nil {
		return res, err
	}
	defer func() { err = errors.Join(err, s.close()) }()
	cfg, err := config.Load(opts.ConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		err = fmt.Errorf("%s does not exist; write it (fleetd init writes the default) and restore again: %w", opts.ConfigPath, err)
	}
	if err != nil {
		return res, s.fail(ReasonConfig, err)
	}
	if err := statedb.EnsureDir(cfg.StateDir); err != nil {
		return res, s.fail(dirReason(err), err)
	}
	l, err := lock.Acquire(filepath.Join(cfg.StateDir, LockName))
	if errors.Is(err, lock.ErrHeld) {
		err = fmt.Errorf("%s: %w; stop fleetd run before restoring", cfg.StateDir, err)
	}
	if err != nil {
		return res, s.fail(lockReason(err), err)
	}
	defer func() { err = errors.Join(err, l.Release()) }()
	if err := s.removeLeftovers(cfg.StateDir); err != nil {
		return res, s.fail(ReasonStore, err)
	}
	who := api.LocalUser(uint32(os.Getuid()), nil) //nolint:gosec // G115: a uid fits in 32 bits on macOS and Linux
	return restore.Run(ctx, restore.Options{
		Actor: statedb.Actor{
			ID: who.GetId().GetValue(), Kind: strings.TrimPrefix(who.GetKind().String(), "PRINCIPAL_KIND_"), Name: who.GetName(),
		},
		StateDir: cfg.StateDir, Backup: req.Backup, Address: req.Address, DefaultPort: agentPort(cfg.Listen.Agent),
		Names: func(addrs []string) ([]string, error) {
			names, _, err := opts.serverNames(cfg, addrs)
			return names, err
		},
		Now: opts.Now, Telemetry: s.tel,
	})
}

// agentPort is listen.agent's port, or restore.DefaultAgentPort when it has
// none or asks the OS to choose (0).
func agentPort(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 1 || n > 65535 {
		return restore.DefaultAgentPort
	}
	return port
}
