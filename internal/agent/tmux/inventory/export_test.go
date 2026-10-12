package inventory

import "time"

// SetReplyBoundForTest sets each client's reply bound in bytes, so a test
// can make an inventory read fail (ErrBadReply) while the short handshake
// reply still fits.
func SetReplyBoundForTest(o *Options, n int) { o.maxReply = n }

// SetReadTimeoutForTest sets the bound on each inventory read, so a test
// can force timeouts fast, and returns a restore to the previous bound.
func SetReadTimeoutForTest(d time.Duration) (restore func()) {
	old := readTimeout
	readTimeout = d
	return func() { readTimeout = old }
}
