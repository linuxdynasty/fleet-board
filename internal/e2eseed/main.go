package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/seed"
)

// run builds a runnable seed fleet: `fleetd init` on the config for a real
// CA identity, then the shared seed rows for the machines. It takes the
// fleetd.yaml path, prints the state folder back, and mirrors the cmd/*
// wiring shape: exit 0, 1 when init or the seed fails, 2 for bad usage.
// Seeding twice fails; the seed is never replaced.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: e2eseed <fleetd.yaml>")
		return 2
	}
	opts := daemon.Options{
		ConfigPath: args[0], Version: "e2e",
		SecretOutput: io.Discard, LogOutput: io.Discard,
	}
	if err := daemon.Init(ctx, opts); err != nil {
		_, _ = fmt.Fprintf(stderr, "e2eseed: %v\n", err)
		return 1
	}
	body, err := os.ReadFile(args[0]) //nolint:gosec // G703: the config path is the operator's own argv, like fleetd --config
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "e2eseed: read config: %v\n", err)
		return 1
	}
	cfg, err := config.Parse(body)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "e2eseed: parse config: %v\n", err)
		return 1
	}
	st, err := statedb.Open(ctx, cfg.StateDir, nil)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "e2eseed: open store: %v\n", err)
		return 1
	}
	defer func() { _ = st.Close() }()
	if err := seed.SeedRows(ctx, st); err != nil {
		_, _ = fmt.Fprintf(stderr, "e2eseed: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, cfg.StateDir)
	return 0
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
