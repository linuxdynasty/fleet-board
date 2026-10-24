package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/release"
)

// upgradeCommand is `fleetd upgrade [--version TAG]`: fetch the release,
// check its checksum, swap the binary and restart the service (02j). A
// checksum mismatch or an older release installs nothing; the databases
// migrate forward only when the new binary starts.
func upgradeCommand(stdout io.Writer) *cobra.Command {
	var want, repo string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Fetch the next release, verify its checksum, swap the binary and restart",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := buildinfo.Current("fleetd")
			if repo == "" {
				repo = release.DefaultRepo
			}
			err := release.Upgrade(cmd.Context(), release.UpgradeOptions{
				Program: release.ProgramFleetd,
				Repo:    repo,
				Current: p.Version,
				Want:    want,
				Fetch:   release.GitHub{},
				Swap:    release.FileSwapper{},
				Restart: release.ServiceRestarter{},
			})
			if err != nil {
				return commandError{err}
			}
			_, err = fmt.Fprintf(stdout, "fleetd: upgraded to %s\n", wantOrLatest(want))
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&want, "version", "", "release tag to install (default: the latest release)")
	f.StringVar(&repo, "repo", release.DefaultRepo, "owner/name holding the releases")
	return cmd
}

func wantOrLatest(want string) string {
	if want == "" {
		return "the latest release"
	}
	return want
}
