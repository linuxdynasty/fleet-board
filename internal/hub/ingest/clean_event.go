package ingest

import (
	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Caps on the Event free text the hub scrubs (02e "Size limits").
const (
	maxParseMessage = 1024
	maxProbeMessage = 256
)

// CleanEvent makes ev's free text safe, in place, on the stream's ingest
// goroutine before the writer job (never in the transaction): the two
// Event fields the agent fills with its own words (row 5 ruling, fork 9:
// UsageReading.parse_message, cap 1 KiB, and ProbeHealth.message, 256
// bytes) lose their control characters, are cut to their cap and only then
// go to the shared scrubber, so a hostile long string costs what a short
// one does (04b). The other strings of an Event are the rows that apply
// its kind to clean (rows 7 to 10), as for [Clean].
func CleanEvent(ev *agentv1.Event) Cleaned {
	var c Cleaned
	if u := ev.GetUsageReading(); u != nil {
		u.ParseMessage = c.scrubTo(u.GetParseMessage(), maxParseMessage)
	}
	if p := ev.GetProbeHealth(); p != nil {
		p.Message = c.scrubTo(p.GetMessage(), maxProbeMessage)
	}
	return c
}
