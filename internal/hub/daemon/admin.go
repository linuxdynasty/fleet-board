package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"os"

	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// bindAdmin opens <state_dir>/admin.sock (02j). Call it with fleetd.lock
// held: a socket already there belongs to a hub that died (SIGKILL leaves
// it), so it is removed first; anything else at that path is refused and
// left alone. The new socket is chmod 0600 at once, and closing the
// listener removes it.
func bindAdmin(cfg *config.Config, opts Options, st *store.Store, id statedb.Identity, s *session) (listener, error) {
	path := config.AdminSocketPath(cfg.StateDir)
	if err := removeStaleSocket(path); err != nil {
		return listener{}, err
	}
	uid := os.Getuid()
	if uid < 0 || uid > math.MaxUint32 {
		return listener{}, fmt.Errorf("daemon: admin socket: user id %d is out of range", uid)
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return listener{}, fmt.Errorf("daemon: admin socket %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return listener{}, errors.Join(fmt.Errorf("daemon: chmod admin socket: %w", err), ln.Close())
	}
	fleet, err := api.NewFleet(api.FleetOptions{Store: st, CACert: id.CACert, Telemetry: s.tel, Now: opts.Now})
	if err != nil {
		return listener{}, errors.Join(err, ln.Close())
	}
	wrapped, srv := api.NewAdmin(ln, api.AdminOptions{
		Fleet: fleet, Telemetry: s.tel, UID: uint32(uid), PeerUID: opts.PeerUID,
	})
	s.tel.Logger.Info("hub.listener_opened", "listener", "admin")
	return listener{name: "admin", ln: wrapped, srv: srv}, nil
}

func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("daemon: admin socket: %w", err)
	case fi.Mode().Type() != fs.ModeSocket:
		return fmt.Errorf("daemon: %s exists and is not a socket; move it away", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("daemon: remove leftover admin socket: %w", err)
	}
	return nil
}
