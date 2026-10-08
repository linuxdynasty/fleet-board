package protoversion

// The proto versions this build speaks, inclusive. The v1 wire is version 1
// (02l); 0 means unset.
const (
	Min uint32 = 1
	Max uint32 = 1
)

// Negotiate picks the highest version both sides speak, given the peer's
// inclusive [peerMin, peerMax]. An unset bound (0), a reversed range, or a
// range that misses [Min, Max] has none: ok is false.
func Negotiate(peerMin, peerMax uint32) (version uint32, ok bool) {
	if peerMin == 0 || peerMax == 0 || peerMin > peerMax {
		return 0, false
	}
	lo, hi := max(peerMin, Min), min(peerMax, Max)
	if lo > hi {
		return 0, false
	}
	return hi, true
}
