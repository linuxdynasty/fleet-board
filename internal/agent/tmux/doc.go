// Package tmux is fleet-agent's tmux control mode (tmux -C, 01 E2): the
// protocol parser and the client.
//
// [Parser] turns the bytes a control client prints into a closed set of
// [Message] values (a [Reply] per command, [Output], [Pause], [Continue],
// [Exit], [SubscriptionChanged], [WindowAdd], [WindowClose],
// [WindowRenamed], [SessionChanged], [SessionsChanged], [Unknown] and
// [Discarded]). It starts no process and writes nothing.
//
// [Client] attaches one `tmux -C` client to one server with ignore-size and
// pause-after, checks both in client_flags (tmux before 3.2 is [ErrTooOld]),
// keeps itself attached ([Client.Run]: backoff while the server is there,
// [StateAbsent] with no timer until [Client.Wake]), and sends only typed
// commands: [Client.Send] of a typing.Line, [Client.Capture],
// [Client.ListPanes], [Client.ReadInventory] (every session, window and
// pane with names and @fleet_* options, for the inventory package; tmux
// bounds each text field at [MaxFieldBytes] in bytes and sends a longer one
// empty with its length, so no value can fail the read),
// [Client.ReadClients] (every attached client for E13, 01 E13),
// [Client.Subscribe] and [Client.Continue]. [Client.Changed] lets a watcher
// follow its state with no timer, and [Client.SubscriptionConfirmed] says
// whether a subscription's first value arrived on this attach. Its file
// conn.go is the only one in the agent that starts the long-lived tmux
// process; one-shot tmux calls go through internal/proc. The README has the
// concurrency diagram. [Pane], in both reads ([PaneInfo] embeds it),
// reports a dead pane's exit status or the signal that killed it, the
// signal as a closed set ([SignalKill] and the rest, or [SignalOther] and
// its number). The signal fails open: text the client cannot read is
// [SignalOther] with no number, never a bad reply.
//
// The parser's rules, from tmux's man page and two recorded transcripts
// (tmux 3.2a and 3.7c, testdata/):
//
//   - A reply is the lines between "%begin T N F" and the "%end T N F" or
//     "%error T N F" with the same three fields. Reply lines are raw command
//     output, which can be pane text and can start with "%", so any other
//     line inside a reply is body.
//   - That includes "%pause %N" and "%continue %N": tmux writes them inside
//     the reply to the client's own "refresh-client -A", but a capture of a
//     pane can print the same text, and only the client knows which command
//     a reply answers. [Reply.ControlNotes] finds them; the client may
//     honour a pause from any reply (the safe direction) but a continue only
//     from its own refresh-client -A reply for that pane.
//   - "%output" values are decoded (\ooo is one byte); "%extended-output"
//     adds the age; "%subscription-changed" keeps its value byte for byte.
//     The client must never use a subscription name with a space: tmux
//     accepts one, and the line is then misread. Window and session names in
//     "%window-renamed" and "%session-changed" are kept byte for byte.
//   - A line over [Options.MaxLineBytes] is skipped to its newline and
//     counted, so memory stays bounded whatever tmux or a pane sends; a reply
//     body over [Options.MaxReplyBytes] (each line charged its bytes plus
//     [ReplyLineOverhead]) keeps its first lines and counts the rest in
//     [Reply.Dropped].
//
// Parse failures, and inventory text fields left blank, are counted and
// logged through [Metrics], and the client's state through
// fleet_agent_tmux_control_up and the tmux.attached, tmux.exit and
// tmux.subscription_confirmed log events: closed words, ids and byte counts
// only, never pane bytes, names or values.
package tmux
