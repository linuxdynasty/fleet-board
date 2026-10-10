package proc

import (
	"bytes"
	"io"
	"os"
	"sync"
)

// pipes are the child's stdout and stderr. Run makes the pipes itself, and
// passes the write ends straight to the child as files, so that os/exec starts
// no copy goroutine of its own. Run's goroutines copy the read ends, and each
// finishes at end-of-file, which comes only when every process that holds a
// write end has closed it or died. That is how Run knows a descendant still
// holds the output, whatever the child's exit status was.
type pipes struct {
	outR, outW, errR, errW *os.File
	done                   chan struct{} // closed when both copies have ended
}

func openPipes() (*pipes, error) {
	p := &pipes{done: make(chan struct{})}
	var err error
	if p.outR, p.outW, err = os.Pipe(); err != nil {
		return nil, err
	}
	if p.errR, p.errW, err = os.Pipe(); err != nil {
		p.closeAll()
		return nil, err
	}
	return p, nil
}

// closeWriters closes this process's copies of the write ends; after the child
// has started they must not stay open here, or end-of-file would never come.
func (p *pipes) closeWriters() {
	_ = p.outW.Close()
	_ = p.errW.Close()
}

// closeReaders ends the copies where they stand, for a descendant that is
// still holding the output past the WaitDelay.
func (p *pipes) closeReaders() {
	_ = p.outR.Close()
	_ = p.errR.Close()
}

func (p *pipes) closeAll() {
	for _, f := range []*os.File{p.outR, p.outW, p.errR, p.errW} {
		if f != nil {
			_ = f.Close()
		}
	}
}

// copyTo starts the two copies, into the buffers and within the budget.
func (p *pipes) copyTo(b *budget, stdout, stderr *bytes.Buffer) {
	var wg sync.WaitGroup
	for _, c := range []struct {
		r   *os.File
		dst *bytes.Buffer
	}{{p.outR, stdout}, {p.errR, stderr}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(b.writer(c.dst), c.r) // a read error is a closed pipe: the end of the output either way
		}()
	}
	go func() {
		wg.Wait()
		close(p.done)
	}()
}
