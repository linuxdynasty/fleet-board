package inventory

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// A failed read is logged as tmux.inventory_failed with a closed err_kind:
// a reply cut short or not in the asked shape (ErrBadReply, the error a
// broken or oversize record gives) is "reply".
func TestReadErrKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want string
	}{
		{fmt.Errorf("tmux: inventory: %w", tmux.ErrBadReply), "reply"},
		{fmt.Errorf("tmux: inventory: %w", tmux.ErrCommandFailed), "command"},
		{context.DeadlineExceeded, "timeout"},
		{errors.New("broken pipe"), "io"},
	}
	for _, tc := range tests {
		if got := readErrKind(tc.err); got != tc.want {
			t.Errorf("readErrKind(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}
