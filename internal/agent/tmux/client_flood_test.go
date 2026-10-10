package tmux_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

// stallFor is how long the test stops the reader while a pane floods: past
// pause-after (1 s), so tmux may pause the pane during the command. A %pause
// cannot be provoked reliably (spike 0.5 finding 7), so nothing waits for one.
const stallFor = 3 * time.Second

// A pane floods while the reader is stalled and a command is waiting for its
// reply. The command still gets its own reply, the stream stays in step, and
// the test reports where any %pause landed (the 7c1 open question: does tmux
// write an age-triggered %pause inside another command's reply?). With
// FLEET_TMUX_RECORD=<dir> the raw stream is kept there.
func TestFloodDuringACommand(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			srv := tmuxtest.Start(t, b, "-s", "work", "exec cat")
			raw := filepath.Join(t.TempDir(), "raw.txt")
			if dir := os.Getenv("FLEET_TMUX_RECORD"); dir != "" {
				raw = filepath.Join(dir, "flood-"+b.Name+".txt")
			}
			f, err := os.Create(raw) //nolint:gosec // G304: the test's own temp file or the folder FLEET_TMUX_RECORD names
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()

			n := newNotes()
			stalled, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			n.stall = func(m tmux.Message) {
				if o, ok := m.(tmux.Output); ok && len(o.Data) > 0 {
					once.Do(func() { close(stalled); <-release })
				}
			}
			r := startClientWith(t, b.Path, srv.Socket, n, func(c *tmux.Client) { tmux.SetTeeForTest(c, f) })
			r.waitState(t, tmux.StateUp)

			srv.Run(t, "new-window", "-t", "work", "exec yes flood-line")
			<-stalled
			type res struct {
				panes []tmux.Pane
				err   error
			}
			got := make(chan res, 1)
			go func() {
				p, err := r.ListPanes(ctxT(t))
				got <- res{p, err}
			}()
			time.Sleep(stallFor) // the stall itself, not a wait for an outcome
			close(release)
			out := <-got
			if out.err != nil {
				t.Fatalf("ListPanes during the flood: %v", out.err)
			}
			if len(out.panes) != 2 {
				t.Fatalf("ListPanes during the flood = %d panes, want 2", len(out.panes))
			}
			srv.Run(t, "kill-window", "-t", "work:1")
			// The stream is in step after the flood.
			if _, err := r.ListPanes(ctxT(t)); err != nil {
				t.Fatalf("ListPanes after the flood: %v", err)
			}
			if err := f.Sync(); err != nil {
				t.Fatal(err)
			}
			inReply, outside := pausesIn(t, raw)
			t.Logf("tmux %s: %%pause lines inside a reply: %d; %%pause notifications: %d", b.Name, inReply, outside)
			if inReply > 0 {
				t.Logf("tmux %s wrote an age-triggered %%pause inside another command's reply", b.Name)
			}
		})
	}
}

// pausesIn parses a recorded stream and counts %pause lines inside replies
// and %pause notifications.
func pausesIn(t *testing.T, path string) (inReply, outside int) {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: a recorded transcript the test names
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p := tmux.NewParser(f, tmux.Options{})
	for {
		m, err := p.Next()
		if errors.Is(err, io.EOF) {
			return inReply, outside
		}
		if err != nil {
			t.Fatal(err)
		}
		switch m := m.(type) {
		case tmux.Reply:
			for _, note := range m.ControlNotes() {
				if _, ok := note.(tmux.Pause); ok {
					inReply++
				}
			}
		case tmux.Pause:
			outside++
		}
	}
}

// The recorded flood transcripts (testdata/flood-*.txt, the first 30 lines
// of a TestFloodDuringACommand run with FLEET_TMUX_RECORD): the list-panes
// reply tmux wrote while the pane flooded and the reader was stalled is
// whole, in order, and holds no %pause line.
func TestRecordedFloodTranscripts(t *testing.T) {
	for _, v := range []string{"3.2a", "3.7c"} {
		t.Run(v, func(t *testing.T) {
			path := filepath.Join("testdata", "flood-"+v+".txt")
			inReply, _ := pausesIn(t, path)
			if inReply != 0 {
				t.Fatalf("%d %%pause lines inside a reply", inReply)
			}
			f, err := os.Open(path) //nolint:gosec // G304: a testdata file named here
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			p := tmux.NewParser(f, tmux.Options{})
			var mine []tmux.Reply
			outputs := 0
			for {
				m, err := p.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				switch m := m.(type) {
				case tmux.Reply:
					if m.Flags&1 == 1 {
						mine = append(mine, m)
					}
				case tmux.Output:
					outputs++
				case tmux.Discarded:
					t.Fatalf("discarded: %+v", m)
				}
			}
			// The handshake, then list-panes with both panes, between
			// flood output on both sides.
			if len(mine) != 2 || len(mine[1].Body) != 2 || outputs < 10 {
				t.Fatalf("replies %+v, %d outputs", mine, outputs)
			}
		})
	}
}
