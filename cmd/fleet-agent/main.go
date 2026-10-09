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

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
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

func (e commandError) Unwrap() error { return e.err }

// run returns the exit code: 0, 1 when a command fails, 2 for bad usage.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	p := buildinfo.Current("fleet-agent")
	root := &cobra.Command{
		Use:           "fleet-agent",
		Short:         "fleet-agent is the fleet-board agent, one per machine",
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
	var stateDir string
	root.PersistentFlags().StringVar(&stateDir, "state-dir", "",
		"agent state folder (default ~/.local/state/fleet-agent)")
	dir := func() (string, error) {
		if stateDir != "" {
			return stateDir, nil
		}
		return conn.DefaultDir()
	}
	root.AddCommand(enrollCommand(dir, p.Version, stdout, stderr))

	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "fleet-agent: %v\n", err)
	var ce commandError
	if errors.As(err, &ce) {
		return 1
	}
	_, _ = fmt.Fprintln(stderr, "run fleet-agent --help for usage")
	return 2
}
