package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// approveCommands are `fleet-agent approve|deny <command_id>` (01f L3):
// the person at the machine answers a command waiting for local approval.
// The id is the command_id of the AWAITING_LOCAL_CONFIRM progress: the
// agent logs it, and the dashboard shows it on the waiting operation.
func approveCommands(dir func() (string, error), stdout io.Writer) []*cobra.Command {
	decide := func(approved bool) func(*cobra.Command, []string) error {
		return func(_ *cobra.Command, args []string) error {
			d, err := dir()
			if err != nil {
				return commandError{err}
			}
			if err := exec.Decide(d, args[0], approved); err != nil {
				return commandError{err}
			}
			verb := "denied"
			if approved {
				verb = "approved"
			}
			_, _ = fmt.Fprintf(stdout, "%s %s: it takes effect at the agent's next approval check\n", args[0], verb)
			return nil
		}
	}
	approve := &cobra.Command{
		Use:   "approve <command_id>",
		Short: "Let one waiting command run",
		Args:  cobra.ExactArgs(1),
		RunE:  decide(true),
	}
	deny := &cobra.Command{
		Use:   "deny <command_id>",
		Short: "Refuse one waiting command",
		Args:  cobra.ExactArgs(1),
		RunE:  decide(false),
	}
	return []*cobra.Command{approve, deny}
}
