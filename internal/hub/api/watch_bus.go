package api

import (
	"sync"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// This file is the Watch fan-out bus (02c): the bounded change log resume
// replays from, the subscriber set live streams read from, and the
// latest-wins Potter for pane summaries and the fleet summary. The store
// assigns hub_seq ([NextStamp] in statedb); the bus never mints one, it only
// remembers the highest it published so a new stream can snapshot the
// position. Everything here is in-memory: a restart mints a new
// hub_instance_id, so every old resume token ends as HUB_RESTARTED.

// watchChange is one committed change offered to Watch streams. match tests
// the new state against a subscription filter, was the old state; both close
// over the List views, so live filtering reuses the 02c matchers. A nil
// match means everything; a nil was means the old state is unknown and no
// UNMATCHED is sent for that subscription.
type watchChange struct {
	topic  hubv1.WatchTopic
	change hubv1.ChangeType // CREATED, UPDATED or DELETED
	// msg carries the resource. hub_seq and resume_token are stamped per
	// send, so the bus never stores them: a send clones this message.
	msg   *hubv1.WatchResponse
	match func(*filterNode) bool
	was   func(*filterNode) bool
}

// droppable reports whether a slow consumer may lose this change. Only the
// latest-wins topics shed; every other topic ends the stream with
// RESYNC_REQUIRED/FELL_BEHIND instead (02c delivery classes).
func (c watchChange) droppable() bool {
	return c.topic == hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES ||
		c.topic == hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY
}

// paneKeyOf names the pane a summary is the latest of: the owning machine
// plus the canonical PaneKey form (ids.proto), so two summaries of one
// pane collapse. The machine is part of the key because PaneKey is unique
// only within one machine: two machines reporting the same server, start
// and pane tuple must not share a version counter or overwrite each other
// in the latest-pane map.
func paneKeyOf(m *hubv1.WatchResponse) string {
	p := m.GetPaneSummary()
	k := p.GetPane()
	return p.GetMachine().GetValue() + "/" + k.GetServer() + "/" + k.GetServerStartId() + "/" + k.GetPaneId()
}

// watchEntry is a logged change with its commit time for the 10-minute
// retention floor.
type watchEntry struct {
	at     time.Time
	seq    uint64
	change watchChange
}

const (
	// watchLogCap bounds the change log (02c: at least 10 minutes or
	// 100,000 changes; the age floor is watchLogKeep).
	watchLogCap = 100000
	// watchLogKeep is the age floor of the change log.
	watchLogKeep = 10 * time.Minute
	// watchSubBuf bounds one stream's unread live changes. Past it a
	// never-dropped change ends the stream behind instead of queuing.
	watchSubBuf = 256
)

// watchSub is one stream's bus subscription.
type watchSub struct {
	ch chan watchEntry
	mu sync.Mutex
	// topics are the stream's subscribed topics: the bus drops every
	// other topic before queueing, so an unsubscribed burst never marks
	// the stream behind and an unsubscribed shed value never leaks in.
	topics map[hubv1.WatchTopic]bool
	// behind is set when a never-dropped change found ch full: the
	// stream must end with RESYNC_REQUIRED/FELL_BEHIND.
	behind bool
	// panes and summary hold droppable changes shed while ch was full;
	// the session merges them latest-wins.
	panes   map[string]watchEntry
	summary *watchEntry
}

func newWatchSub(topics []hubv1.WatchTopic) *watchSub {
	s := &watchSub{ch: make(chan watchEntry, watchSubBuf), panes: map[string]watchEntry{}}
	for _, t := range topics {
		if s.topics == nil {
			s.topics = map[hubv1.WatchTopic]bool{}
		}
		s.topics[t] = true
	}
	return s
}

// wants reports whether the stream subscribes to the topic. The caller
// holds the bus mutex.
func (s *watchSub) wants(topic hubv1.WatchTopic) bool {
	return s.topics[topic]
}

// isBehind reports the flag. The caller holds no lock.
func (s *watchSub) isBehind() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.behind
}

// drainStash takes the shed changes and clears them. The caller holds no
// lock.
func (s *watchSub) drainStash() (panes map[string]watchEntry, summary *watchEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	panes, s.panes = s.panes, map[string]watchEntry{}
	summary, s.summary = s.summary, nil
	return panes, summary
}

// watchBus fans committed changes out to Watch streams and keeps the
// bounded log resume replays from.
type watchBus struct {
	mu     sync.Mutex
	now    func() time.Time
	logCap int
	// seq is the highest hub_seq published. Store stamps only rise, so
	// this is the snapshot position a new stream reads initial state at.
	seq uint64
	// evicted is the highest hub_seq eviction removed, 0 when nothing
	// ever evicted. A resume token at or above it is replayable even
	// when it sits below the oldest retained entry: hub_seq gaps are
	// normal (02c), so only eviction proves a range is gone.
	evicted uint64
	// log holds the never-dropped topics oldest first.
	log  []watchEntry
	subs map[*watchSub]struct{}
	// panes is the latest summary per pane key; summary the latest fleet
	// summary. Both back resume without a store read.
	panes   map[string]watchEntry
	summary *watchEntry
}

func newWatchBus(now func() time.Time, logCap int) *watchBus {
	if now == nil {
		now = time.Now
	}
	if logCap <= 0 {
		logCap = watchLogCap
	}
	return &watchBus{now: now, logCap: logCap, subs: map[*watchSub]struct{}{}, panes: map[string]watchEntry{}}
}

// publish appends a never-dropped change to the log and offers it to the
// streams subscribed to its topic. The caller's seq is the store stamp,
// published in commit order (the Fleet serializes commit-then-publish, so
// concurrent handlers cannot append backwards). It never blocks: a full
// stream is flagged behind (RESYNC_REQUIRED/FELL_BEHIND) instead of
// queueing, and the entry is dropped for it.
func (b *watchBus) publish(e watchEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e.seq > b.seq {
		b.seq = e.seq
	}
	b.log = append(b.log, e)
	b.evict()
	for s := range b.subs {
		if !s.wants(e.change.topic) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.markBehindLocked()
		}
	}
}

// publishPane records the latest summary of one pane and offers it to the
// streams subscribed to it, shedding silently on a full one. Pane
// summaries never enter the change log (02c). A stale summary never
// overwrites a newer one: concurrent publishers still arrive in commit
// order, but the guard keeps the map exact if one ever does not.
func (b *watchBus) publishPane(key string, e watchEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if prev, dup := b.panes[key]; !dup || e.seq >= prev.seq {
		b.panes[key] = e
	}
	for s := range b.subs {
		if !s.wants(e.change.topic) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.stashLocked(e)
		}
	}
}

// publishSummary records the latest fleet summary and offers it to the
// streams subscribed to it, shedding silently on a full one. Like panes
// it never enters the log; the session paces sends to one per second
// (02c). A stale summary never overwrites a newer one, as for panes.
func (b *watchBus) publishSummary(e watchEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.summary == nil || e.seq >= b.summary.seq {
		b.summary = &e
	}
	for s := range b.subs {
		if !s.wants(e.change.topic) {
			continue
		}
		select {
		case s.ch <- e:
		default:
			s.stashLocked(e)
		}
	}
}

// markBehindLocked flags the stream behind; the bus mutex is held.
func (s *watchSub) markBehindLocked() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.behind = true
}

// stashLocked keeps a shed change; the bus mutex is held.
func (s *watchSub) stashLocked(e watchEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.change.topic == hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES {
		s.panes[paneKeyOf(e.change.msg)] = e
	} else {
		c := e
		s.summary = &c
	}
}

// evict drops log entries past both guarantees: an entry leaves only
// when the log holds more than the count cap AND the entry is older than
// the age floor, so the log keeps the larger of the two (02c: at least 10
// minutes or 100,000 changes). The caller holds b.mu.
func (b *watchBus) evict() {
	now := b.now()
	for len(b.log) > b.logCap && len(b.log) > 0 && now.Sub(b.log[0].at) > watchLogKeep {
		if b.log[0].seq > b.evicted {
			b.evicted = b.log[0].seq
		}
		b.log = b.log[1:]
	}
	// A shift left while something still references the slice keeps the
	// whole backing array alive; copy when the head ran far ahead.
	if len(b.log) == 0 {
		b.log = nil
	}
}

// evictedSeq is the highest hub_seq eviction removed, 0 when nothing ever
// evicted: the resume floor. The caller holds no lock.
func (b *watchBus) evictedSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.evicted
}

// subscribe adds a stream on topics and returns its subscription with the
// snapshot position: entries after seq arrive on the channel.
func (b *watchBus) subscribe(topics []hubv1.WatchTopic) (*watchSub, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := newWatchSub(topics)
	b.subs[s] = struct{}{}
	return s, b.seq
}

// unsubscribe removes a stream. Entries already received stay readable.
func (b *watchBus) unsubscribe(s *watchSub) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, s)
}

// snapshot returns the log entries after from up to to, the oldest retained
// seq (0 when the log is empty), and the newest retained seq. The caller
// holds no lock.
func (b *watchBus) snapshot(from, to uint64) (entries []watchEntry, oldest, newest uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.log) > 0 {
		oldest, newest = b.log[0].seq, b.log[len(b.log)-1].seq
	}
	for _, e := range b.log {
		if e.seq > from && e.seq <= to {
			entries = append(entries, e)
		}
	}
	return entries, oldest, newest
}

// latest returns the newest published seq, for tokens on messages that carry
// no change (checkpoints). The caller holds no lock.
func (b *watchBus) latest() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// latestPanes returns the current summary per pane. The caller holds no
// lock.
func (b *watchBus) latestPanes() map[string]watchEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]watchEntry, len(b.panes))
	for k, e := range b.panes {
		out[k] = e
	}
	return out
}
