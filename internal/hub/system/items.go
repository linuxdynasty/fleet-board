package system

import (
	"strings"

	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Op is what the writer must do with one SYSTEM triage item.
type Op int

const (
	// OpRaise opens a new SYSTEM item for a signal at or above its 05b
	// "Item raised at" level with no open item yet.
	OpRaise Op = iota + 1
	// OpUpdate refreshes the open item in place: a flapping signal is
	// one item whose severity changes in place, never a new one.
	OpUpdate
	// OpRetire closes the open item: the signal is back to OK (after
	// the Evaluator's five-minute return-to-OK delay).
	OpRetire
)

// OpenItem is one currently-open SYSTEM triage item, as the writer read
// it: the dedupe key, the severity it shows, and the detail it shows.
type OpenItem struct {
	Fingerprint string
	Severity    hubv1.Severity
	Detail      *hubv1.SystemDetail
}

// Change is one SYSTEM item write for the writer. A raise or update
// carries everything the triage seed needs but the id (a ULID at write
// time): the dedupe key, the severity, a one-line title in closed words,
// the SystemDetail, and the subject ids (an integration subject rides in
// the detail: triage items have no integration column). A retire carries
// only the key.
type Change struct {
	Op          Op
	Fingerprint string
	Severity    hubv1.Severity
	Title       string
	Detail      *hubv1.SystemDetail
	Machine     *typev1.MachineId
	Account     *typev1.AccountId
}

// Decide maps one round of evaluated verdicts plus the open SYSTEM items
// to the writes that round needs (05b section 8, "What a signal raises").
// A verdict at or above its signal's RaiseAt opens an item when none is
// open and refreshes it when the severity or the detail behind it moved;
// anything else (below the raise level, UNKNOWN with no reading yet)
// keeps an open item without a write, and only OK retires it. The same
// inputs always decide the same writes, so a round that changes nothing
// writes nothing. Subjects missing from the round are left alone: a
// deleted machine never reports OK, so absence must not retire its item;
// the subject's own teardown owns that.
func Decide(verdicts []Verdict, open []OpenItem) []Change {
	byFingerprint := make(map[string]OpenItem, len(open))
	for _, o := range open {
		byFingerprint[o.Fingerprint] = o
	}
	var out []Change
	for _, v := range verdicts {
		fp := Fingerprint(v.Kind, subjectOf(v))
		o, has := byFingerprint[fp]
		switch {
		case v.State == hubv1.SystemState_SYSTEM_STATE_OK:
			if has {
				out = append(out, Change{Op: OpRetire, Fingerprint: fp})
			}
		case ShouldRaise(v.Kind, v.State):
			sev := SeverityFor(v.Kind, v.State)
			d := detailOf(v)
			c := Change{
				Op: OpRaise, Fingerprint: fp, Severity: sev,
				Title: titleFor(v.Kind, v.State), Detail: d,
				Machine: v.Machine, Account: v.Account,
			}
			if !has {
				out = append(out, c)
			} else if o.Severity != sev || !proto.Equal(o.Detail, d) {
				c.Op = OpUpdate
				out = append(out, c)
			}
		default:
			// Below the raise level, or UNKNOWN: the open item
			// stays (ShouldKeep) with no write.
		}
	}
	return out
}

// subjectOf is the fingerprint subject of one verdict: the machine,
// account or integration id, or the hub-wide subject.
func subjectOf(v Verdict) string {
	switch {
	case v.Machine != nil:
		return v.Machine.GetValue()
	case v.Account != nil:
		return v.Account.GetValue()
	case v.Integration != nil:
		return v.Integration.GetValue()
	default:
		return HubSubject
	}
}

// detailOf builds the verdict's SystemDetail: which signal, how bad, and
// the full measure that set the state with the reading behind it, for
// the triage card.
func detailOf(v Verdict) *hubv1.SystemDetail {
	var m *hubv1.SystemMeasure
	for _, c := range Measures(v.Kind) {
		if c.Kind == v.SetBy {
			m = MeasureProto(c, v.reading(c.Kind))
			break
		}
	}
	return &hubv1.SystemDetail{
		Signal: v.Kind, State: v.State, Measure: m,
		Integration: v.Integration,
	}
}

// warnTitles names each raisable WARNING state in closed plain words for
// the item's one-line title. The six CRITICAL-only signals never raise at
// WARNING, so they have no entry.
var warnTitles = map[hubv1.SystemSignalKind]string{
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED:       "machine has gone quiet",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS: "usage readings are stale",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG:      "hub writer is lagging",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE:             "disk space is low",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE:            "backups are getting old",
}

// critTitles names each raisable CRITICAL state the same way. DISK_FREE
// and WRITER_BATCH_LAG can lose work, matching their SEVERITY_CRITICAL.
var critTitles = map[hubv1.SystemSignalKind]string{
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_AGENT_CONNECTED:       "machine is offline",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_USAGE_PROBE_FRESHNESS: "usage readings are long stale",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_INTEGRATION_SYNC_LAG:  "integration sync is lagging",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WRITER_BATCH_LAG:      "hub writer is stalled",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_OUTBOX_BACKLOG:        "commands are piling up",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_WATCH_DROPS:           "dashboard streams are dropping",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_LOG_STORE_USAGE:       "log store is over its cap",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_DISK_FREE:             "disk space is critically low",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_CLOCK_SKEW:            "machine clock disagrees",
	hubv1.SystemSignalKind_SYSTEM_SIGNAL_KIND_BACKUP_AGE:            "no recent backup",
}

// titleFor is the item's one-line title for one state: the closed words
// above, or the signal and state names when a new signal has no entry
// yet (still closed: enums only, never readings or ids).
func titleFor(kind hubv1.SystemSignalKind, state hubv1.SystemState) string {
	name := "system " + strings.ToLower(shortName(kind))
	switch state {
	case hubv1.SystemState_SYSTEM_STATE_WARNING:
		if s, ok := warnTitles[kind]; ok {
			return s
		}
		return name + " warning"
	case hubv1.SystemState_SYSTEM_STATE_CRITICAL:
		if s, ok := critTitles[kind]; ok {
			return s
		}
		return name + " critical"
	case hubv1.SystemState_SYSTEM_STATE_OK,
		hubv1.SystemState_SYSTEM_STATE_UNKNOWN,
		hubv1.SystemState_SYSTEM_STATE_UNSPECIFIED:
		return name + " needs attention"
	}
	return name + " needs attention"
}
