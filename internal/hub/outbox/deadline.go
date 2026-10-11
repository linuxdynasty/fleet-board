package outbox

import "time"

// Default deadlines (02-contracts "Deadlines", defaults.md "Command
// deadline").
const (
	deadlineInteractive = 2 * time.Minute
	deadlineDispatch    = 30 * time.Minute
	deadlineStopProbe   = 24 * time.Hour
	deadlineOther       = 10 * time.Minute
)

// DefaultDeadline is how long after the enqueue a command of kind (its
// arm name, [KindName]) is due when the caller set no not_after_time: 2
// min for the interactive kinds, 30 min for the dispatch kinds, 24 h for
// stop_probe (its machine may be asleep), 10 min for every other kind.
func DefaultDeadline(kind string) time.Duration {
	switch kind {
	case "send_keys", "interrupt", "run_harness_command", "capture_pane", "open_attach":
		return deadlineInteractive
	case "start_goal", "ensure_worktree", "prune_worktree", "ensure_goal_hooks":
		return deadlineDispatch
	case "stop_probe":
		return deadlineStopProbe
	default:
		return deadlineOther
	}
}
