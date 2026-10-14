package tmux_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestFloodQuarantinedVersion checks which tmux releases the flood test
// skips: only tmux 3.8, whose control server wedges under machine load when
// a command is in flight while a pane floods (#208). Every other release,
// and anything the test cannot read, runs the flood.
func TestFloodQuarantinedVersion(t *testing.T) {
	for _, tc := range []struct {
		name string
		ver  string
		want bool
	}{
		{name: "quarantined 3.8", ver: "tmux 3.8", want: true},
		{name: "trailing newline", ver: "tmux 3.8\n", want: true},
		{name: "pinned latest 3.7c runs", ver: "tmux 3.7c", want: false},
		{name: "oldest 3.2a runs", ver: "tmux 3.2a", want: false},
		{name: "newer runs", ver: "tmux 3.9", want: false},
		{name: "prerelease runs", ver: "tmux next-3.8", want: false},
		{name: "empty runs", ver: "", want: false},
		{name: "garbage runs", ver: "not tmux", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := floodQuarantinedVersion(tc.ver); got != tc.want {
				t.Fatalf("floodQuarantinedVersion(%q) = %v, want %v", tc.ver, got, tc.want)
			}
		})
	}
}

// floodForceEnv runs the flood even on a quarantined tmux: set it to re-probe
// a release (for example a tmux 3.8 build with a fix) without editing the test.
const floodForceEnv = "FLEET_TMUX_FLOOD_FORCE"

// floodQuarantinedVersion reports whether the flood test skips a tmux that
// prints ver as the first line of `tmux -V`: only tmux 3.8, whose control
// server wedges under machine load when a command is in flight while a pane
// floods and the reader stalls (no %pause, no reply, the server burns CPU;
// #208). Anything else, and anything unreadable, runs: a skip must name a
// proven release, so the flood keeps its coverage on 3.2a and 3.7c.
func floodQuarantinedVersion(ver string) bool {
	f := strings.Fields(ver)
	return len(f) == 2 && f[0] == "tmux" && f[1] == "3.8"
}

// floodBinaryVersion runs `program -V` (it contacts no server) and returns
// its first line, or "" when tmux cannot tell: an unreadable version never
// quarantines.
func floodBinaryVersion(program string) string {
	if program == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, program, "-V").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}

// A pane floods while the reader is stalled and a command is waiting for its
// reply. The command still gets its own reply, the stream stays in step, and
// the test reports where any %pause landed (the 7c1 open question: does tmux
// write an age-triggered %pause inside another command's reply?). With
// FLEET_TMUX_RECORD=<dir> the raw stream is kept there.
func TestFloodDuringACommand(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			if ver := floodBinaryVersion(b.Path); floodQuarantinedVersion(ver) && os.Getenv(floodForceEnv) == "" {
				t.Skipf("flood skips %s: its control server wedges under load with a command in flight during a flood (no reply, no %%pause; #208); coverage stays on 3.2a and 3.7c, set %s=1 to re-probe", ver, floodForceEnv)
			}
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
				t.Fatalf("ListPanes during the flood: %v (%s)", out.err, floodSummary(t, raw))
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

// TestFloodSummary checks the one-line failure evidence a flood run logs:
// what tmux wrote before the command failed, so a recurrence of the 3.8
// stall (#208) carries how much output arrived, how fresh the last of it
// was, whether tmux ever paused, and how many own replies came back.
func TestFloodSummary(t *testing.T) {
	handshake := "%begin 1791888111 317 0\n%end 1791888111 317 0\n" +
		"%begin 1791888111 319 1\nattached,focused,control-mode,ignore-size,pause-after=1,UTF-8|1|2|3.8\n%end 1791888111 319 1\n"
	listReply := "%begin 1791888111 320 1\n$0 @0 %0 0   80 23\n$0 @0 %1 0   80 23\n%end 1791888111 320 1\n"
	for _, tc := range []struct {
		name   string
		stream string
		want   string
	}{
		{
			name: "stalled",
			stream: handshake +
				"%extended-output %1 0 : flood-line\\015\\012\n" +
				"%extended-output %1 1 : flood-line\\015\\012\n" +
				"%extended-output %1 5 : flood-line\\015\\012\n",
			want: "3 outputs (last age 5ms), 0 %pause, 0 %continue, 1 own reply",
		},
		{
			name: "healthy",
			stream: handshake +
				"%extended-output %1 0 : flood-line\\015\\012\n" +
				"%pause %1\n" +
				listReply,
			want: "1 output (last age 0s), 1 %pause, 0 %continue, 2 own replies",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "raw.txt")
			if err := os.WriteFile(path, []byte(tc.stream), 0o600); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("stream %d bytes: %s", len(tc.stream), tc.want)
			if got := floodSummary(t, path); got != want {
				t.Fatalf("floodSummary = %q, want %q", got, want)
			}
		})
	}
	t.Run("missing", func(t *testing.T) {
		got := floodSummary(t, filepath.Join(t.TempDir(), "absent.txt"))
		want := "stream unreadable"
		if len(got) < len(want) || got[:len(want)] != want {
			t.Fatalf("floodSummary = %q, want prefix %q", got, want)
		}
	})
}

// floodSummary reads back the recorded stream after a flood command failed:
// its size, how much output arrived and how fresh the last of it was,
// whether tmux ever paused, and how many of the client's own replies came
// back. Pane text is never quoted, only counted.
func floodSummary(t *testing.T, path string) string {
	t.Helper()
	fi, err := os.Stat(path) //nolint:gosec // G703: a recorded transcript the test names
	if err != nil {
		return fmt.Sprintf("stream unreadable: %v", err)
	}
	f, err := os.Open(path) //nolint:gosec // G304: a recorded transcript the test names
	if err != nil {
		return fmt.Sprintf("stream unreadable: %v", err)
	}
	defer func() { _ = f.Close() }()
	var outputs, pauses, continues, replies int
	var lastAge time.Duration
	p := tmux.NewParser(f, tmux.Options{})
	for {
		m, err := p.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Sprintf("stream %d bytes, parse stopped: %v", fi.Size(), err)
		}
		switch m := m.(type) {
		case tmux.Reply:
			if m.Flags&1 == 1 {
				replies++
			}
			for _, note := range m.ControlNotes() {
				if _, ok := note.(tmux.Pause); ok {
					pauses++
				}
			}
		case tmux.Output:
			outputs++
			if m.Extended {
				lastAge = m.Age
			}
		case tmux.Pause:
			pauses++
		case tmux.Continue:
			continues++
		}
	}
	replyWord := "replies"
	if replies == 1 {
		replyWord = "reply"
	}
	outputWord := "outputs"
	if outputs == 1 {
		outputWord = "output"
	}
	return fmt.Sprintf("stream %d bytes: %d %s (last age %s), %d %%pause, %d %%continue, %d own %s",
		fi.Size(), outputs, outputWord, lastAge, pauses, continues, replies, replyWord)
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

// The recorded stalled transcript (testdata/flood-stalled-3.8.txt, the
// first 30 lines of a TestFloodDuringACommand run on tmux 3.8 under machine
// load whose ListPanes never got a reply, #208): the handshake shows
// pause-after=1, flood output arrives fresh (ages 0 to 1 ms), and then the
// wedge signature: no %pause anywhere and no second own reply. The full
// 110-line stream ends the same way, mid-flood, with the last age 5 ms.
func TestRecordedStalledTranscript(t *testing.T) {
	path := filepath.Join("testdata", "flood-stalled-3.8.txt")
	f, err := os.Open(path) //nolint:gosec // G304: a testdata file named here
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p := tmux.NewParser(f, tmux.Options{})
	var own, outputs, pauses int
	var lastAge time.Duration
	for {
		m, err := p.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch m := m.(type) {
		case tmux.Reply:
			if m.Flags&1 == 1 {
				own++
			}
			for _, note := range m.ControlNotes() {
				if _, ok := note.(tmux.Pause); ok {
					pauses++
				}
			}
		case tmux.Output:
			outputs++
			if m.Extended {
				lastAge = m.Age
			}
		case tmux.Pause:
			pauses++
		case tmux.Discarded:
			t.Fatalf("discarded: %+v", m)
		}
	}
	// The handshake's own reply, flood output, and nothing else: tmux never
	// paused the pane and never answered list-panes.
	if own != 1 || outputs < 10 || pauses != 0 {
		t.Fatalf("own replies %d, outputs %d, %%pauses %d", own, outputs, pauses)
	}
	if lastAge > time.Second {
		t.Fatalf("last output age %s: the stream went stale, not wedged fresh", lastAge)
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
