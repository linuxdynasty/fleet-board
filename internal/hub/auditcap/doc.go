// Package auditcap decides which refused Enrolls get a machine.enroll_failed
// audit row (02g, defaults.md, the row 5 ruling's fork 12): one row per
// (reason, peer) per minute, PerWindow rows per minute in all, and one
// [Summary] per minute for the rest, with the reason counts, the distinct
// peers and the suppressed count. A pair's next row carries how many of its
// refusals were suppressed since its last one.
//
// The state is in memory only. A restart starts it empty, so in the minute
// of a restart at most PerWindow more rows and one more summary can be
// written; the summary of the minute in progress is taken by [Cap.Flush]
// when the hub stops. A summary is handed out by the next [Cap.Note] after
// its minute, or by Flush, never by a timer: on a quiet hub it waits for
// the next refusal or the stop. Every refusal is still counted by
// fleet_hub_enroll_total{result="refused"} and logged as enroll.refused.
package auditcap
