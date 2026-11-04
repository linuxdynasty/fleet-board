// Package attach is the agent side of terminal attach (J1): one grouped
// view session and PTY client per browser attach, bridged to the hub's
// Terminal stream by the Manager, driven by the OpenAttach and CloseAttach
// command handlers.
//
// Opening an attach creates a grouped view session
// (`new-session -t <session> -s fleet-view-<attach>`) and spawns
// `tmux attach` on a PTY, then waits for the client to land before arming
// the view's self-destruct (`destroy-unattached`): arming an empty view
// lets tmux reap it before a slow attach lands. Only then does the
// manager pair the Terminal stream (ready), start the pumps, and answer
// the view name. The window strip and the developer's prefix key both
// switch the view session's current window, never the developer's own.
// Closing kills the PTY client, the view destroys itself, and the target
// session and its panes keep running. Output strips the escape sequences
// that act on the viewer's machine (OSC 52 clipboard writes, hyperlinks);
// input is enforced read-only for viewer attaches here (plus `-f
// read-only` on the client) as well as on the hub.
package attach
