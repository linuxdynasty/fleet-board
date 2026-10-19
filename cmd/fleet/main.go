package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/fleetcli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(fleetcli.Run(ctx, os.Args[1:], fleetcli.Input{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		LookupEnv: os.LookupEnv, Version: buildinfo.Version,
	}))
}
