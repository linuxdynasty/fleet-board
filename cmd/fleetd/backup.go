package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
)

// backupCommand is `fleetd backup [--encrypt]`: stdout gets only the path
// of the copy, so `FILE=$(fleetd backup)` works. The passphrase is read from
// stdin only when it is a terminal; there is no flag or variable for it.
func backupCommand(options func() (daemon.Options, error), stdin *os.File, stdout, stderr io.Writer) *cobra.Command {
	var req daemon.BackupRequest
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Copy state.db into the state folder's backups/ (it holds the CA key: keep it secret); works with or without fleetd run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := options()
			if err != nil {
				return commandError{err}
			}
			opts.Terminal = backup.NewTerminal(stdin, stderr)
			res, err := daemon.Backup(cmd.Context(), opts, req)
			if err != nil {
				return commandError{err}
			}
			if res.Encrypted {
				if _, err := fmt.Fprintln(stderr, "fleetd: keep the passphrase: without it this backup cannot be restored"); err != nil {
					return commandError{err}
				}
			}
			if _, err := fmt.Fprintln(stdout, res.Path); err != nil {
				return commandError{err}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&req.Encrypt, "encrypt", false,
		"encrypt the copy with age under a passphrase typed twice at the terminal")
	return cmd
}
