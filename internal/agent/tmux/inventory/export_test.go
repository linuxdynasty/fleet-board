package inventory

import "time"

// SetReplyBoundForTest sets each client's reply bound in bytes, so a test
// can make an inventory read fail (ErrBadReply) while the short handshake
// reply still fits.
func SetReplyBoundForTest(o *Options, n int) { o.maxReply = n }

// SetInputPollForTest sets the backup list-clients poll gap, so a test
// can watch the poll run and stop fast. The production gap stays 2 s.
func SetInputPollForTest(o *Options, d time.Duration) { o.inputPoll = d }

// SetInputGapForTest sets the per-pane PaneInput gate, so a test can
// prove the gate without waiting out the 10 s contract. The contract
// value stays 10 s (defaultInputGap, pinned by TestInputGapDefault).
func SetInputGapForTest(o *Options, d time.Duration) { o.emitGap = d }

// SetReadTimeoutForTest sets the bound on each inventory read, so a test
// can force timeouts fast, and returns a restore to the previous bound.
func SetReadTimeoutForTest(d time.Duration) (restore func()) {
	old := readTimeout
	readTimeout = d
	return func() { readTimeout = old }
}
