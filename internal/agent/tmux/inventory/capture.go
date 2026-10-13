package inventory

import (
	"context"
	"fmt"
)

// maxCaptureBytes clamps an E10 capture's text (command_tmux.proto
// CapturePane.max_bytes: the agent clamps to 256 KiB).
const maxCaptureBytes = 256 * 1024

// CapturePane reads one pane's visible screen for E10: the text row 18's
// CapturePane command answers with. lastLines 0 is the whole screen, else
// the last N rows; keepANSI keeps the escape sequences, else they are
// stripped; maxBytes 0 or past the clamp uses the clamp. The text is
// capped and control-stripped, never scrubbed (02e, row-7 ruling). It
// returns the text, whether anything was cut, or an error: an unknown
// server, a malformed pane id, or a failed capture. Pane ids restart with
// a new server, so the caller passes the key it read the pane under; this
// never guesses across servers.
func (o *Observer) CapturePane(ctx context.Context, server, pane string, lastLines uint32, keepANSI bool, maxBytes uint32) (string, bool, error) {
	s := o.servers[server]
	if s == nil {
		return "", false, fmt.Errorf("inventory: capture: unknown server %q", server)
	}
	id, ok := parsePaneID(pane)
	if !ok {
		return "", false, fmt.Errorf("inventory: capture: bad pane id %q", pane)
	}
	got, err := s.c.Capture(ctx, id)
	if err != nil {
		return "", false, fmt.Errorf("inventory: capture: %w", err)
	}
	lines, truncated := got.Lines, got.Truncated
	if lastLines > 0 && uint32(len(lines)) > lastLines { //nolint:gosec // reply-bound lines fit uint32
		lines = lines[len(lines)-int(lastLines):]
		truncated = true
	}
	max := int(maxBytes)
	if max <= 0 || max > maxCaptureBytes {
		max = maxCaptureBytes
	}
	text, cut := renderScreen(lines, keepANSI, max)
	return text, truncated || cut, nil
}
