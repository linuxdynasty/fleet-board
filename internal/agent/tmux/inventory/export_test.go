package inventory

// SetReplyBoundForTest sets each client's reply bound in bytes, so a test
// can make an inventory read fail (ErrBadReply) while the short handshake
// reply still fits.
func SetReplyBoundForTest(o *Options, n int) { o.maxReply = n }
