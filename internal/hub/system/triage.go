package system

import (
	"strings"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// HubSubject is the fingerprint subject for hub-wide signals (05b section 8:
// WRITER_BATCH_LAG, WATCH_DROPS, LOG_STORE_USAGE, DISK_FREE, BACKUP_AGE).
// Per-signal subjects are the machine, account or integration id.
const HubSubject = "hub"

// shortName strips the SYSTEM_SIGNAL_KIND_ prefix: SYSTEM_SIGNAL_KIND_DISK_FREE
// becomes DISK_FREE. It stays in the closed 05b set, never free text.
func shortName(kind hubv1.SystemSignalKind) string {
	name, ok := hubv1.SystemSignalKind_name[int32(kind)]
	if !ok {
		return "UNSPECIFIED"
	}
	return strings.TrimPrefix(name, "SYSTEM_SIGNAL_KIND_")
}

// Fingerprint is the deduplication key of one SYSTEM triage item (05b
// section 8): system:<signal>:<subject>. One open item per key, so a
// flapping signal changes severity in place instead of opening a new item.
// Pass HubSubject for hub-wide signals; a machine, account or integration id
// otherwise. An empty subject reads as hub rather than leaving a blank key.
func Fingerprint(kind hubv1.SystemSignalKind, subject string) string {
	if subject == "" {
		subject = HubSubject
	}
	return "system:" + shortName(kind) + ":" + subject
}

// RaiseAt is the lowest state that opens a SYSTEM item (05b section 8, "Item
// raised at"): WARNING for AGENT_CONNECTED, USAGE_PROBE_FRESHNESS, DISK_FREE
// and BACKUP_AGE; CRITICAL for the other six. WARNING on a CRITICAL-only
// signal stays shown without an item.
func RaiseAt(kind hubv1.SystemSignalKind) hubv1.SystemState {
	switch kind {
	case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE,
		hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE:
		return hubv1.SystemState_SYSTEM_STATE_WARNING
	default:
		return hubv1.SystemState_SYSTEM_STATE_CRITICAL
	}
}

// ShouldRaise reports whether state opens the signal's SYSTEM item: at or
// above RaiseAt. OK and UNKNOWN never open; UNKNOWN (no reading yet) is
// shown as unknown and opens nothing.
func ShouldRaise(kind hubv1.SystemSignalKind, state hubv1.SystemState) bool {
	switch state {
	case hubv1.SystemState_SYSTEM_STATE_WARNING,
		hubv1.SystemState_SYSTEM_STATE_CRITICAL:
		return state >= RaiseAt(kind)
	default:
		return false
	}
}

// ShouldKeep reports whether an open SYSTEM item stays open. Any WARNING,
// CRITICAL or UNKNOWN keeps it; only OK retires it, and the Evaluator's
// five-minute return-to-OK delay decides when OK counts (05b section 8,
// Hysteresis; 03d: the item retires when the signal returns to OK). A
// CRITICAL-only signal that falls back to WARNING keeps its item with the
// severity moved in place, and a signal that loses its reading keeps its
// item instead of retiring unknown.
func ShouldKeep(state hubv1.SystemState) bool {
	switch state {
	case hubv1.SystemState_SYSTEM_STATE_WARNING,
		hubv1.SystemState_SYSTEM_STATE_CRITICAL,
		hubv1.SystemState_SYSTEM_STATE_UNKNOWN:
		return true
	default:
		return false
	}
}

// SeverityFor maps a signal's state to its triage severity (05b section 8,
// "What a signal raises"): WARNING opens SEVERITY_WARNING, CRITICAL opens
// SEVERITY_ACTION, except DISK_FREE and WRITER_BATCH_LAG at CRITICAL, which
// can lose work and open SEVERITY_CRITICAL. OK and UNKNOWN map to
// SEVERITY_UNSPECIFIED: no item.
func SeverityFor(kind hubv1.SystemSignalKind, state hubv1.SystemState) hubv1.Severity {
	switch state {
	case hubv1.SystemState_SYSTEM_STATE_WARNING:
		return hubv1.Severity_SEVERITY_WARNING
	case hubv1.SystemState_SYSTEM_STATE_CRITICAL:
		switch kind {
		case hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE,
			hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG:
			return hubv1.Severity_SEVERITY_CRITICAL
		default:
			return hubv1.Severity_SEVERITY_ACTION
		}
	default:
		return hubv1.Severity_SEVERITY_UNSPECIFIED
	}
}
