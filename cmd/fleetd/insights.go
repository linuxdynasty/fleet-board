package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
)

// insightsCommand is `fleetd insights`: the subscription-efficiency
// rollups behind GetInsights (05 section 2).
func insightsCommand(options func() (daemon.Options, error), stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "insights",
		Short: "Subscription-efficiency rollups behind GetInsights",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	var from string
	rebuild := &cobra.Command{
		Use:   "rebuild --from YYYY-MM-DD",
		Short: "Recompute the efficiency rollup rows from a UTC day on (fleetd run must be stopped)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" {
				return commandError{errors.New("fleetd insights rebuild needs --from YYYY-MM-DD")}
			}
			day, err := time.Parse(time.DateOnly, from)
			if err != nil {
				return commandError{fmt.Errorf("fleetd insights rebuild needs --from YYYY-MM-DD, got %q", from)}
			}
			opts, err := options()
			if err != nil {
				return commandError{err}
			}
			res, err := daemon.InsightsRebuild(cmd.Context(), opts, daemon.InsightsRebuildRequest{From: day})
			if err != nil {
				return commandError{err}
			}
			_, err = fmt.Fprintf(stdout, "fleetd: rebuilt %d windows for %d accounts from %s\n",
				res.Windows, res.Accounts, day.Format(time.DateOnly))
			if err != nil {
				return commandError{err}
			}
			return nil
		},
	}
	rebuild.Flags().StringVar(&from, "from", "", "first UTC day to recompute (YYYY-MM-DD)")
	cmd.AddCommand(rebuild)
	return cmd
}
