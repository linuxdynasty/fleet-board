// Package tmux reads tmux control mode (tmux -C, 01 E2) for fleet-agent.
//
// This first part is the protocol parser only: [Parser] turns the bytes a
// control client prints into a closed set of [Message] values (a [Reply] per
// command, [Output], [Pause], [Continue], [Exit], [SubscriptionChanged],
// [Unknown] and [Discarded]). It starts no process and writes nothing; the
// client that runs tmux and sends commands comes later (plan row 7).
//
// The parser's rules, from tmux's man page and two recorded transcripts
// (tmux 3.2a and 3.7c, testdata/):
//
//   - A reply is the lines between "%begin T N F" and the "%end T N F" or
//     "%error T N F" with the same three fields. Reply lines are raw command
//     output, which can be pane text and can start with "%", so any other
//     line inside a reply is body.
//   - The one exception is a well-formed "%pause %N" or "%continue %N": tmux
//     writes them inside the reply to "refresh-client -A", and the parser
//     returns them on their own before the reply.
//   - "%output" values are decoded (\ooo is one byte); "%extended-output"
//     adds the age; "%subscription-changed" keeps its value byte for byte.
//     Subscription names must hold no space.
//   - A line over [Options.MaxLineBytes] is skipped to its newline and
//     counted, so memory stays bounded whatever tmux or a pane sends; a reply
//     body over [Options.MaxReplyBytes] keeps its first lines and counts the
//     rest in [Reply.Dropped].
//
// Failures are counted and logged through [Metrics] with an error class and
// byte counts only, never pane bytes.
package tmux
