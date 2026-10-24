package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// commandError marks a failure of a command that started, as opposed to a
// usage error that cobra found in the arguments.
type commandError struct{ err error }

func (e commandError) Error() string { return e.err.Error() }

// run returns the exit code: 0, 1 when a command fails, 2 for bad usage.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	p := buildinfo.Current("fleetd")
	root := &cobra.Command{
		Use:           "fleetd",
		Short:         "fleetd is the fleet-board hub",
		Version:       p.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	var configFlag string
	root.PersistentFlags().StringVar(&configFlag, "config", "",
		"fleetd.yaml to read (default $FLEETD_CONFIG, then ~/.config/fleetd/fleetd.yaml)")
	options := func() (daemon.Options, error) {
		path, err := config.Path(configFlag, os.Getenv)
		return daemon.Options{ConfigPath: path, Version: p.Version, LogOutput: stderr}, err
	}
	root.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Write the default fleetd.yaml if missing, the state folder and state.db with the hub identity",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := options()
			if err == nil {
				err = daemon.Init(cmd.Context(), opts)
			}
			if err != nil {
				return commandError{err}
			}
			_, err = fmt.Fprintf(stdout, "fleetd: initialized with %s; start the hub with: fleetd run\n", opts.ConfigPath)
			return err
		},
	}, &cobra.Command{
		Use:   "run",
		Short: "Start the hub; SIGTERM stops it cleanly",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts, err := options()
			if err == nil {
				err = daemon.Run(cmd.Context(), opts)
			}
			if err != nil {
				return commandError{err}
			}
			return nil
		},
	}, enrollCommand(options, stdout, stderr), backupCommand(options, os.Stdin, stdout, stderr), restoreCommand(options, stdout),
		insightsCommand(options, stdout), setupCommand(options, stdout, stderr), upgradeCommand(stdout))

	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "fleetd: %v\n", err)
	var ce commandError
	if errors.As(err, &ce) {
		return 1
	}
	_, _ = fmt.Fprintln(stderr, "run fleetd --help for usage")
	return 2
}
