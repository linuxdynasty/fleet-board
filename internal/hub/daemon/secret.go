package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
)

// showSecretOnce creates the local approval secret when it is missing and
// shows a new one once at the terminal (ADR 0032): fleetd init prints it
// for the owner to enter into the dashboard, and the first start of an
// older store prints it the same way. out is Options.SecretOutput (stderr
// when nil); the secret never reaches the structured log, only the
// secret_created event without it.
func showSecretOnce(ctx context.Context, dir string, out io.Writer, log *slog.Logger) error {
	created, err := secret.Ensure(dir, nil)
	if err != nil {
		return fmt.Errorf("daemon: approval secret: %w", err)
	}
	if !created {
		return nil
	}
	shown, err := secret.Reveal(dir)
	if err != nil {
		return fmt.Errorf("daemon: approval secret: %w", err)
	}
	if out == nil {
		out = os.Stderr
	}
	if _, err := fmt.Fprintln(out, "fleetd: local approval secret (shown once; enter it into the dashboard session):"); err != nil {
		return fmt.Errorf("daemon: approval secret: show: %w", err)
	}
	if _, err := fmt.Fprintln(out, shown); err != nil {
		return fmt.Errorf("daemon: approval secret: show: %w", err)
	}
	log.InfoContext(ctx, "approval.secret_created")
	return nil
}
