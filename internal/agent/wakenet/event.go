package wakenet

import (
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
)

// Event is one wake or one confirmed network change. Reason is
// CONNECT_REASON_WAKE or CONNECT_REASON_NETWORK_CHANGE; Wake is set for the
// first, Net for the second. Pass it to [Decide] to learn what to do.
type Event struct {
	Reason agentv1.ConnectReason
	Wake   WakeInfo
	Net    NetInfo
}

// WakeInfo says which wake signals fired in one check. Both may fire at once;
// that is still one event.
type WakeInfo struct {
	// AsleepGrew: the time asleep since boot grew by 2 s or more.
	AsleepGrew  bool
	AsleepDelta time.Duration
	// WallAhead: the wall clock moved 2 s or more past Go's monotonic clock.
	WallAhead bool
	WallGap   time.Duration
}

// Source says what made the network watcher read the interfaces.
type Source string

const (
	// SourcePush is a debounced routing push message.
	SourcePush Source = "push"
	// SourcePoll is the 15 s fallback poll, used only while the push socket is off.
	SourcePoll Source = "poll"
	// sourceStart is the first reading, taken when the watcher starts; never in an Event.
	sourceStart Source = "start"
)

// NetInfo is a confirmed network change: what differs between the last
// reading and this one. Changes is never empty in an event. The readings are
// the caller's own copies.
type NetInfo struct {
	Source   Source
	Changes  []Change
	Previous Reading
	Current  Reading
}
