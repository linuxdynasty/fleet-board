package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
)

// restoreCommand is `fleetd restore <backup> [--address A]`: offline, it
// takes fleetd.lock, so it is refused while fleetd run is up.
func restoreCommand(options func() (daemon.Options, error), stdout io.Writer) *cobra.Command {
	var req daemon.RestoreRequest
	cmd := &cobra.Command{
		Use:   "restore BACKUP",
		Short: "Replace state.db with a backup (fleetd run must be stopped); the old state.db is kept beside it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, err := options()
			if err != nil {
				return commandError{err}
			}
			req.Backup = args[0]
			res, err := daemon.Restore(cmd.Context(), opts, req)
			if !res.Replaced { // nothing changed
				if err == nil {
					err = errors.New("the restore did not replace state.db")
				}
				return commandError{err}
			}
			// The new store is in place even when a later step (the folder
			// sync, the cleanup) failed; say so, then report the failure.
			kept := "there was no state.db to keep"
			if res.Previous != "" {
				kept = "the replaced state.db is kept as " + res.Previous
			}
			_, perr := fmt.Fprintf(stdout, "fleetd: restored %s; %s.\nAgents re-take settings, and until %s the hub adopts what they know. Start the hub with: fleetd run\n",
				req.Backup, kept, res.ReconcileUntil.UTC().Format(time.RFC3339))
			if err = errors.Join(err, perr); err != nil {
				return commandError{err}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&req.Address, "address", "",
		"add this hub address (host or host:port) to the list agents get, and to the server certificate")
	return cmd
}
