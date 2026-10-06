package main

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
)

// enrollCommand is `fleetd enroll-token`: stdout gets only the token, so
// `TOKEN=$(fleetd enroll-token --machine m)` works; the rest goes to stderr.
func enrollCommand(options func() (daemon.Options, error), stdout, stderr io.Writer) *cobra.Command {
	var req daemon.EnrollRequest
	var ttl time.Duration
	cmd := &cobra.Command{
		Use:   "enroll-token",
		Short: "Ask the running hub for a one-time token that enrolls a machine",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("ttl") {
				req.TTL = &ttl
			}
			opts, err := options()
			if err != nil {
				return commandError{err}
			}
			resp, err := daemon.EnrollToken(cmd.Context(), opts, req)
			if err != nil {
				return commandError{err}
			}
			expire := resp.GetExpireTime().AsTime()
			_, err = fmt.Fprintf(stderr, "fleetd: token for machine %q expires at %s (in %s)\n"+
				"fleetd: CA fingerprint (SHA-256): %s\nfleetd: on the machine, run: %s\n",
				req.Machine, expire.Local().Format(time.RFC3339), time.Until(expire).Round(time.Second),
				resp.GetCaFingerprint(), resp.GetEnrollCommand())
			if err != nil {
				return commandError{err}
			}
			if _, err := fmt.Fprintln(stdout, resp.GetToken()); err != nil {
				return commandError{err}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&req.Machine, "machine", "", "name the machine will get (required)")
	f.StringVar(&req.ExpectHost, "expect-host", "", "hostname the machine should report (default: the --machine name)")
	f.DurationVar(&ttl, "ttl", 0, "token lifetime, more than 0 and at most 24h (default 15m)")
	f.StringArrayVar(&req.Labels, "label", nil, "label the machine starts with; repeat for more")
	f.BoolVar(&req.StartDrained, "start-drained", false, "the machine takes no work until you undrain it")
	_ = cmd.MarkFlagRequired("machine") // the flag is defined just above
	return cmd
}
