package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// doctorDeps are the parts of `doctor` a test replaces.
type doctorDeps struct {
	// probeDir is the usage probe's folder (01 C3).
	probeDir func() (string, error)
	// tmux runs `tmux -V` through the agent's measured proc caller.
	tmux func(ctx context.Context) (string, error)
	// checks is the list to run; nil is doctor.Local.
	checks []doctor.Check
}

// doctorCommand is `fleet-agent doctor`: every local check with a reason, one
// line each (or one JSON object with --json). It reads status.json and
// enroll.json, so it works while the agent is down, and starts nothing but
// `tmux -V`. Exit 0 when nothing failed, 1 when a check failed.
func doctorCommand(dir, cfg func() (string, error), stdout io.Writer) *cobra.Command {
	return newDoctorCommand(dir, cfg, doctorDeps{probeDir: defaultProbeDir, tmux: tmuxVersion}, stdout)
}

func newDoctorCommand(dir, cfg func() (string, error), deps doctorDeps, stdout io.Writer) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine and say what is wrong, with a fix for each problem",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := dir()
			if err != nil {
				return commandError{err}
			}
			cd, err := cfg()
			if err != nil {
				return commandError{err}
			}
			probe, err := deps.probeDir()
			if err != nil {
				return commandError{err}
			}
			checks := deps.checks
			if checks == nil {
				checks = doctor.Local()
			}
			rep := doctor.Run(cmd.Context(), doctor.Options{StateDir: d, ConfigDir: cd, ProbeDir: probe, TmuxVersion: deps.tmux}, checks...)
			write := doctor.WriteText
			if asJSON {
				write = doctor.WriteJSON
			}
			if err := write(stdout, rep); err != nil {
				return commandError{err}
			}
			if rep.Failed() {
				return commandError{fmt.Errorf("%d check(s) failed", rep.Summary.Fail)}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the report as one JSON object")
	return cmd
}

// defaultProbeDir is ~/.local/share/fleet-agent/probe, the same on macOS and
// Linux (01 C3).
func defaultProbeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find the home folder: %w", err)
	}
	return filepath.Join(home, ".local", "share", "fleet-agent", "probe"), nil
}

// tmuxVersion asks tmux for its version the way the machine facts do. The
// runner takes no telemetry: doctor prints to the person who ran it.
func tmuxVersion(ctx context.Context) (string, error) {
	r, err := proc.NewRunner(nil)
	if err != nil {
		return "", err
	}
	return machine.TmuxVersion(ctx, r, nil)
}
