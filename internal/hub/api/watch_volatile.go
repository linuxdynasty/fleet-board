package api

import (
	"slices"
	"time"

	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Volatile-field coalescing (02c): at most one Watch message per resource
// per interval for changes that touch only volatile fields.

// volatileGates holds volatile-only changes inside their 02c interval: at
// most one Watch message per resource per interval, the message carrying
// the latest values. A change to any other field publishes at once and
// carries the held values with it. The caller holds watchMu.
type volatileGates struct {
	last map[string]time.Time
	held map[string]heldEntry
}

// heldEntry is a volatile-only change waiting out its interval; the tick
// flushes it by re-reading the row, so the message carries the latest
// values.
type heldEntry struct {
	topic hubv1.WatchTopic
	id    string
}

func newVolatileGates() *volatileGates {
	return &volatileGates{last: map[string]time.Time{}, held: map[string]heldEntry{}}
}

// gateKey names one gated resource.
func gateKey(topic hubv1.WatchTopic, id string) string {
	return topic.String() + "/" + id
}

// allow reports whether the change publishes now. A non-volatile change
// always does (and clears anything held); a volatile-only one does when
// its interval passed since the last publish, else it is held for the
// tick to flush. The caller holds watchMu.
func (f *Fleet) volatileAllow(topic hubv1.WatchTopic, id string, volatileOnly bool, interval time.Duration) bool {
	if !volatileOnly {
		delete(f.volatile.held, gateKey(topic, id))
		f.volatile.last[gateKey(topic, id)] = f.now()
		return true
	}
	key := gateKey(topic, id)
	if f.now().Sub(f.volatile.last[key]) >= interval {
		delete(f.volatile.held, key)
		f.volatile.last[key] = f.now()
		return true
	}
	f.volatile.held[key] = heldEntry{topic: topic, id: id}
	return false
}

// machineVolatileInterval is the 02c MACHINE volatile interval.
const machineVolatileInterval = 30 * time.Second

// goalVolatileInterval is the 02c GOAL volatile interval.
const goalVolatileInterval = 5 * time.Second

// machineVolatileOnly reports whether the machine write touched only
// volatile fields (02c): the telemetry sample, last-seen and counter
// columns and the stamps. State, identity, drain and policy report at
// once.
func machineVolatileOnly(before, after statedb.MachineRow) bool {
	return before.State == after.State && before.Name == after.Name &&
		slices.Equal(before.Labels, after.Labels) &&
		before.Drained == after.Drained && before.AgentDraining == after.AgentDraining &&
		before.InstallID == after.InstallID && before.Facts == after.Facts &&
		before.Version == after.Version && before.CreatedMillis == after.CreatedMillis &&
		before.PolicyHash == after.PolicyHash && before.PolicyError == after.PolicyError &&
		before.PolicyChanged == after.PolicyChanged && before.PolicySafe == after.PolicySafe
}

// goalVolatileOnly reports whether the goal write touched only volatile
// fields (02c: first_line, head_commit, usage_consumed) or the stamps: the
// two views with the volatile status fields masked compare equal and the
// specs match. A view that does not parse is not volatile-only: the change
// publishes at once and the offer logs the data error.
func goalVolatileOnly(before, after goalView) bool {
	mask := func(v goalView) *hubv1.GoalStatus {
		out, _ := proto.Clone(v.status).(*hubv1.GoalStatus)
		if out == nil {
			return nil
		}
		out.FirstLine, out.HeadCommit, out.UsageConsumed = "", "", nil
		out.ObservedTime, out.HubSeq = nil, 0
		return out
	}
	maskedBefore, maskedAfter := mask(before), mask(after)
	if maskedBefore == nil || maskedAfter == nil {
		return false
	}
	return proto.Equal(maskedBefore, maskedAfter) && before.row.Spec == after.row.Spec
}
