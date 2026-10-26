package logship

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// fakeRunner records the specs it ran and answers a canned error. The seam
// is the production path (*proc.Runner satisfies Runner): every tmux call
// the Tracker makes goes through it, so the assertions below prove the
// exact argv tmux would run.
type fakeRunner struct {
	specs []proc.Spec
	err   error
}

func (f *fakeRunner) Run(_ context.Context, sp proc.Spec) (proc.Result, error) {
	f.specs = append(f.specs, sp)
	return proc.Result{}, f.err
}

func testCaller(t *testing.T) proc.Caller {
	t.Helper()
	caller, err := proc.For("tmux")
	if err != nil {
		t.Fatalf("proc.For(tmux) = %v, want nil", err)
	}
	return caller
}

func testTracker(t *testing.T, r *fakeRunner) *Tracker {
	t.Helper()
	tr, err := New(Options{
		Runner: r,
		Caller: testCaller(t),
		Tmux:   "tmux",
		Socket: "/tmp/fleet-test-sock",
		Server: "fleet",
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New = %v, want nil", err)
	}
	return tr
}

func TestStartRejectsShapelessPane(t *testing.T) {
	for _, tc := range []struct {
		name string
		pane string
	}{
		{name: "empty pane", pane: ""},
		{name: "shell, not a pane id", pane: "0"},
		{name: "missing percent", pane: "3"},
		{name: "non-numeric", pane: "%x"},
		{name: "injection past the id", pane: "%1;touch /tmp/pwned"},
		{name: "path traversal past the id", pane: "%1/../x"},
		{name: "newline past the id", pane: "%1\npipe-pane -t %2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := &fakeRunner{}
			tr := testTracker(t, r)
			if _, err := tr.Start(t.Context(), tc.pane); err == nil {
				t.Fatalf("Start(%q) = nil, want a shape error", tc.pane)
			}
			if len(r.specs) != 0 {
				t.Fatalf("Start(%q) ran %d tmux calls, want none", tc.pane, len(r.specs))
			}
		})
	}
}

func TestStartRunsPipePaneAppend(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	tr := testTracker(t, r)
	pane := "%3"
	path, err := tr.Start(t.Context(), pane)
	if err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	if len(r.specs) != 1 {
		t.Fatalf("Start ran %d tmux calls, want 1", len(r.specs))
	}
	got := r.specs[0]
	if got.Path != "tmux" {
		t.Fatalf("binary = %q, want tmux", got.Path)
	}
	wantArgs := []string{"-S", "/tmp/fleet-test-sock", "pipe-pane", "-o", "-t", pane}
	if len(got.Args) != len(wantArgs)+1 {
		t.Fatalf("args = %q, want %q plus one shell command", got.Args, wantArgs)
	}
	for i, w := range wantArgs {
		if got.Args[i] != w {
			t.Fatalf("args = %q, want %q plus one shell command", got.Args, wantArgs)
		}
	}
	shell := got.Args[len(got.Args)-1]
	if !strings.HasPrefix(strings.TrimSpace(shell), "cat >>") || !strings.Contains(shell, path) {
		t.Fatalf("shell command = %q, want cat append to %q", shell, path)
	}
	// The pane id loses its `%`: tmux expands `%`-sequences in the pipe
	// command (status-left formats), so the raw name carries digits only.
	if !strings.HasSuffix(path, filepath.Join("raw", "fleet", "3.log")) {
		t.Fatalf("raw path = %q, want it under raw/fleet/3.log", path)
	}
	// The raw file is pre-created mode 0600: screens can hold secrets,
	// and the shell redirection would otherwise follow the umask.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat raw file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("raw file mode = %o, want 600", fi.Mode().Perm())
	}
	if !tr.Captured(pane) {
		t.Fatal("Captured = false after Start, want true")
	}
}

func TestStartEscapesForTmuxAndShell(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// A state dir with a space, a quote, and tmux format characters: the
	// whole pipe command stays one argv element, tmux sees no format to
	// expand, and the file still resolves under it.
	dirQuoted := dir + " owner's %100 #W"
	if err := os.MkdirAll(dirQuoted, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &fakeRunner{}
	tr, err := New(Options{
		Runner: r, Caller: testCaller(t), Tmux: "tmux",
		Socket: "/tmp/fleet-test-sock", Server: "fleet", Dir: dirQuoted,
	})
	if err != nil {
		t.Fatalf("New = %v, want nil", err)
	}
	path, err := tr.Start(t.Context(), "%1")
	if err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	if len(r.specs) != 1 || len(r.specs[0].Args) == 0 {
		t.Fatalf("Start ran %v, want one pipe-pane call", r.specs)
	}
	shell := r.specs[0].Args[len(r.specs[0].Args)-1]
	// tmux format escapes first (`%` to `%%`, `#` to `##`), then the
	// whole command is one single-quoted shell word.
	if !strings.Contains(shell, "%%100") || !strings.Contains(shell, "##W") {
		t.Fatalf("shell command = %q, want tmux-escaped %% and #", shell)
	}
	// The shell must see no bare `%`-sequence or `#{`-style format past
	// the quoting: strip the outer quotes and look for singles. A `%%`
	// pair or `##` pair is the escape; a lone `%` or `#` is a live one.
	stripped := strings.TrimSpace(shell)
	if !strings.HasPrefix(stripped, "cat >> '") || !strings.HasSuffix(stripped, "'") {
		t.Fatalf("shell command = %q, want cat append as one quoted word", shell)
	}
	if got := filepath.Dir(path); got != filepath.Join(dirQuoted, "raw", "fleet") {
		t.Fatalf("raw dir = %q, want it under the state dir", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat raw file: %v", err)
	}
}

func TestStopUnknownPaneIsOK(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	tr := testTracker(t, r)
	// Declarative: stopping a capture that was never started is OK, like
	// ReleaseGoal on an absent entry, so a repeated stop never fails.
	if err := tr.Stop(t.Context(), "%9"); err != nil {
		t.Fatalf("Stop unknown pane = %v, want nil", err)
	}
	if len(r.specs) != 0 {
		t.Fatalf("Stop unknown pane ran %d tmux calls, want none", len(r.specs))
	}
}

func TestStartStopRoundTrip(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{}
	tr := testTracker(t, r)
	if _, err := tr.Start(t.Context(), "%2"); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	if err := tr.Stop(t.Context(), "%2"); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
	if tr.Captured("%2") {
		t.Fatal("Captured = true after Stop, want false")
	}
	if len(r.specs) != 2 {
		t.Fatalf("round trip ran %d tmux calls, want 2", len(r.specs))
	}
	stop := r.specs[1].Args
	// A stop names no shell command: tmux then closes the pane's pipe.
	if len(stop) != 5 || stop[2] != "pipe-pane" || stop[3] != "-t" || stop[4] != "%2" {
		t.Fatalf("stop args = %q, want pipe-pane -t with no command", stop)
	}
}

func TestStartPropagatesTmuxFailure(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{err: errors.New("tmux: broken pipe")}
	tr := testTracker(t, r)
	if _, err := tr.Start(t.Context(), "%1"); err == nil {
		t.Fatal("Start = nil, want the tmux failure")
	}
	if tr.Captured("%1") {
		t.Fatal("Captured = true after a failed Start, want false")
	}
}

func TestNewRejectsShapelessOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Options)
	}{
		{name: "no runner", mut: func(o *Options) { o.Runner = nil }},
		{name: "no caller", mut: func(o *Options) { o.Caller = proc.Caller{} }},
		{name: "no socket", mut: func(o *Options) { o.Socket = "" }},
		{name: "no server", mut: func(o *Options) { o.Server = "" }},
		{name: "bad server", mut: func(o *Options) { o.Server = "../x" }},
		{name: "no dir", mut: func(o *Options) { o.Dir = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := Options{
				Runner: &fakeRunner{}, Caller: testCaller(t), Tmux: "tmux",
				Socket: "/tmp/fleet-test-sock", Server: "fleet", Dir: t.TempDir(),
			}
			tc.mut(&o)
			if _, err := New(o); err == nil {
				t.Fatalf("New(%s) = nil, want a shape error", tc.name)
			}
		})
	}
}
