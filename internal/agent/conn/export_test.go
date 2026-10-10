package conn

import "time"

// SetAnchorHook makes the loop report each stream's hub-time anchor (when
// its HelloAck arrived), for tests that check it keeps a monotonic reading.
func SetAnchorHook(o *LoopOptions, f func(hubRecv time.Time)) { o.anchorHook = f }
