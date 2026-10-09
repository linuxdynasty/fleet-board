package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// enrollCommand is `fleet-agent enroll --hub <host[:port]> --token <token>`
// (02g A1). Log lines go to stderr; the outcome is one line on stdout.
func enrollCommand(dir func() (string, error), version string, stdout, stderr io.Writer) *cobra.Command {
	var hub, token string
	cmd := &cobra.Command{
		Use:   "enroll",
		Short: "Join this machine to the fleet with a token from fleetd enroll-token",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dir()
			if err != nil {
				return commandError{err}
			}
			tel, err := telemetry.New(telemetry.Config{
				Service: telemetry.Agent, Version: version, InstanceID: bootID(), LogOutput: stderr,
			})
			if err != nil {
				return commandError{err}
			}
			defer func() { _ = tel.Shutdown(context.WithoutCancel(cmd.Context())) }()
			res, err := conn.Enroll(cmd.Context(), conn.Options{
				Dir: d, Hub: hub, Token: token, Version: version, Telemetry: tel,
			})
			if err != nil {
				return commandError{err}
			}
			note := ""
			if res.Replayed {
				note = " (the hub's answer to an earlier try)"
			}
			_, err = fmt.Fprintf(stdout, "fleet-agent: enrolled as %q (machine %s) in fleet %s through %s%s\n",
				res.Identity.MachineName, res.Identity.MachineID, res.Identity.FleetID, res.HubAddress, note)
			if err != nil {
				return commandError{err}
			}
			if res.HostnameMismatch {
				// The hub records the mismatch too (03 W1); the owner decides.
				_, _ = fmt.Fprintf(stderr, "fleet-agent: warning: the token expected hostname %q but this machine reports %q\n",
					res.Identity.ExpectedHostname, res.Hostname)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&hub, "hub", "", "hub address, host or host:port (port 7443 if none); required until discovery is built")
	f.StringVar(&token, "token", "", "the one-time token from fleetd enroll-token (required)")
	_ = cmd.MarkFlagRequired("token") // the flag is defined just above
	return cmd
}

// bootID is a random id for this process, the telemetry instance id.
func bootID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
