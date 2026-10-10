package proc

import (
	"bytes"
	"io"
	"sync"
	"sync/atomic"
)

// budget is the output cap, shared by stdout and stderr: the two streams may
// together hold at most cap bytes, and nothing past it is kept.
type budget struct {
	mu     sync.Mutex
	left   int64
	over   atomic.Bool
	onOver func()
}

// writer returns a Writer that keeps what fits in dst. The first byte past
// the cap calls onOver once (the kill). After that the writer still accepts
// and drops bytes, so a child blocked on a full pipe cannot stall before the
// kill lands.
func (b *budget) writer(dst *bytes.Buffer) io.Writer { return budgetWriter{b, dst} }

type budgetWriter struct {
	b   *budget
	dst *bytes.Buffer
}

func (w budgetWriter) Write(p []byte) (int, error) {
	w.b.mu.Lock()
	take := min(int64(len(p)), w.b.left)
	w.dst.Write(p[:take])
	w.b.left -= take
	w.b.mu.Unlock()
	if take < int64(len(p)) && !w.b.over.Swap(true) {
		w.b.onOver()
	}
	return len(p), nil
}
