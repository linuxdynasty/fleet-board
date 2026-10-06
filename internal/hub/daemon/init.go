package daemon

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/lock"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// LockName is the one-hub-per-folder lock file in the state folder (#92).
const LockName = "fleetd.lock"

// Init is `fleetd init`: it reads fleetd.yaml (or takes the defaults and
// writes the file when it is missing), creates the state folder (0700),
// takes fleetd.lock, and creates state.db with the whole hub_identity row in
// one atomic step. It never replaces an existing store: state.db already
// there is [ReasonStoreExists] and nothing changes.
func Init(ctx context.Context, opts Options) (err error) {
	s, err := newSession("init", opts)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.close()) }()

	body, readErr := os.ReadFile(opts.ConfigPath)
	missing := errors.Is(readErr, fs.ErrNotExist)
	if readErr != nil && !missing {
		return s.fail(ReasonConfig, fmt.Errorf("read %s: %w", opts.ConfigPath, readErr))
	}
	cfg, err := config.Parse(body)
	if err != nil {
		return s.fail(ReasonConfig, err)
	}
	if err := statedb.EnsureDir(cfg.StateDir); err != nil {
		return s.fail(dirReason(err), err)
	}
	l, err := lock.Acquire(filepath.Join(cfg.StateDir, LockName))
	if err != nil {
		return s.fail(lockReason(err), fmt.Errorf("%s: %w", cfg.StateDir, err))
	}
	defer func() { err = errors.Join(err, l.Release()) }()
	if err := s.removeLeftovers(cfg.StateDir); err != nil {
		return s.fail(ReasonStore, err)
	}

	if _, err := os.Lstat(statedb.Path(cfg.StateDir)); err == nil {
		return s.fail(ReasonStoreExists, fmt.Errorf("%s already exists; init never replaces a store", statedb.Path(cfg.StateDir)))
	}
	if missing {
		if err := config.WriteDefault(opts.ConfigPath); err != nil {
			return s.fail(ReasonConfig, err)
		}
	}
	id, err := newIdentity(cfg, opts)
	if err != nil {
		return s.fail(ReasonIdentity, err)
	}
	err = statedb.Create(ctx, cfg.StateDir, id, statedb.CreateOptions{Telemetry: s.tel})
	switch {
	case errors.Is(err, statedb.ErrExists):
		return s.fail(ReasonStoreExists, err)
	case errors.Is(err, statedb.ErrCreatedNotSynced):
		return s.fail(ReasonStoreNotSynced, fmt.Errorf("%w; it is complete, so check the disk, then start the hub with fleetd run", err))
	case err != nil:
		return s.fail(ReasonStore, err)
	}
	s.tel.Logger.Info("hub.initialized", "server_names", len(id.ServerNames), "config_written", missing)
	return nil
}

// newIdentity mints the hub_identity row: fleet id, Ed25519 CA, the server
// certificate for the configured or default names, a 32-byte account salt
// (salt_version 1) and the settings epoch. hub_addresses stays empty.
func newIdentity(cfg *config.Config, opts Options) (statedb.Identity, error) {
	names, err := opts.names(cfg, ca.DefaultNames)
	if err != nil {
		return statedb.Identity{}, err
	}
	now := opts.now()
	auth, err := ca.New(now, rand.Reader)
	if err != nil {
		return statedb.Identity{}, err
	}
	keyDER, err := auth.KeyDER()
	if err != nil {
		return statedb.Identity{}, err
	}
	sc, err := auth.IssueServer(names, now, rand.Reader)
	if err != nil {
		return statedb.Identity{}, err
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return statedb.Identity{}, fmt.Errorf("daemon: account salt: %w", err)
	}
	fleet, err := newUUID()
	if err != nil {
		return statedb.Identity{}, err
	}
	epoch, err := newUUID()
	if err != nil {
		return statedb.Identity{}, err
	}
	return statedb.Identity{
		FleetID:       fleet,
		CACert:        auth.Cert.Raw,
		CAKey:         keyDER,
		ServerCert:    sc.CertDER,
		ServerKey:     sc.KeyDER,
		ServerNames:   sc.Names,
		AccountSalt:   salt,
		SaltVersion:   1,
		SettingsEpoch: epoch,
		HubAddresses:  []string{},
		CreatedTime:   now,
	}, nil
}

// removeLeftovers deletes a crashed init's temporary files; call it with
// fleetd.lock held. It logs hub.leftovers_removed when it removed any.
func (s *session) removeLeftovers(dir string) error {
	n, err := statedb.RemoveLeftovers(dir)
	if n > 0 {
		s.tel.Logger.Info("hub.leftovers_removed", "command", s.command, "files", n)
	}
	return err
}

func dirReason(err error) Reason {
	switch {
	case errors.Is(err, statedb.ErrLooseMode):
		return ReasonLooseMode
	case errors.Is(err, fs.ErrNotExist):
		return ReasonStoreMissing
	default:
		return ReasonStore
	}
}

func lockReason(err error) Reason {
	if errors.Is(err, lock.ErrHeld) {
		return ReasonLockHeld
	}
	return ReasonStore
}
