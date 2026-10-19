package store

// CommitWake is a coalesced hint that a writer batch committed. The channel
// holds at most one pending hint; a consumer must read committed state to find
// the changes. The writer closes it after the final flush during Close.
// A hub runtime gives this channel to one ordered Watch dispatcher.
func (s *Store) CommitWake() <-chan struct{} { return s.wake }
