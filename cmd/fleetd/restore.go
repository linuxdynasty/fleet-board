package main

import (
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
			if err != nil {
				return commandError{err}
			}
			kept := "there was no state.db to keep"
			if res.Previous != "" {
				kept = "the replaced state.db is kept as " + res.Previous
			}
			_, err = fmt.Fprintf(stdout, "fleetd: restored %s; %s.\nAgents re-take settings, and until %s the hub adopts what they know. Start the hub with: fleetd run\n",
				req.Backup, kept, res.ReconcileUntil.UTC().Format(time.RFC3339))
			return err
		},
	}
	cmd.Flags().StringVar(&req.Address, "address", "",
		"add this hub address (host or host:port) to the list agents get, and to the server certificate")
	return cmd
}
