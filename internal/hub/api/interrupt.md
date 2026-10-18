# Interrupt routing

`InterruptPane` puts one command requiring `tmux.default_server.interrupt`
version 1 in the durable outbox. `FleetOptions.OnCommandQueued` notifies the
agent listener's outbox service only after a new row definitely commits. An
API request for another server is refused before an outbox row exists. The
accepted writer job is awaited even when the request context ends, since it
may still commit. Request-id replay returns the same operation without
notifying again. Only a session already mapped in `hub_sessions` can be
routed; arbitrary developer sessions await the session mirror.
`CreateSession` currently writes a row but its `EnsureSession` command has no
production handler, so it does not create a usable pane for this slice. The
integration proof seeds a row that `InterruptPane` resolves through its normal
production query; it does not establish an ordinary end-user route yet. The
observed-session mirror that will supply that route is #40.
