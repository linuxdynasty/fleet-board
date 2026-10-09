// Package scrub is the shared secret scrubber for agent-written free text
// (02e, "Size limits"): CommandResult.message, CommandProgress.message,
// parse_message and ProbeHealth.message, and the hub's stored copies.
//
// [Text] removes tokens, emails, URL query values and passwords in URLs,
// writes the caller's home folder as "~" when the caller names it, and cuts
// the result to the field's cap on a UTF-8 boundary. It is a pure function:
// the agent calls it before sending and the hub calls it again before storing,
// and text already scrubbed comes out the same. To make that hold, it repeats
// its redaction round until a round changes nothing (a bounded number of
// times; text that has not settled by then is replaced whole).
//
// The token shapes it knows are listed in README.md with the reason for each.
package scrub
