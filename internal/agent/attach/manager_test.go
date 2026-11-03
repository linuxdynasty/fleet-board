package attach

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// fakeStream is the manager's hub: test-fed downs, recorded ups, and a
// send gate that models a slow browser (HTTP/2 flow control holding sends
// until the gate closes or the dial context ends).
type fakeStream struct {
	mu       sync.Mutex
	sent     []*agentv1.TerminalRequest
	sendGate chan struct{}
	recv     chan *agentv1.TerminalResponse
	dialCtx  context.Context
}

func (f *fakeStream) Send(req *agentv1.TerminalRequest) error {
	select {
	case <-f.dialCtx.Done():
		return f.dialCtx.Err()
	default:
	}
	if f.sendGate != nil {
		select {
		case <-f.sendGate:
		case <-f.dialCtx.Done():
			return f.dialCtx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, req)
	return nil
}

func (f *fakeStream) Receive() (*agentv1.TerminalResponse, error) {
	select {
	case <-f.dialCtx.Done():
		return nil, f.dialCtx.Err()
	case r, ok := <-f.recv:
		if !ok {
			return nil, errors.New("fake: hub closed the stream")
		}
		return r, nil
	}
}

func (f *fakeStream) ups() []*agentv1.TerminalRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*agentv1.TerminalRequest(nil), f.sent...)
}

func (f *fakeStream) outputs() []string {
	var out []string
	for _, up := range f.ups() {
		if len(up.GetUp().GetOutput()) > 0 {
			out = append(out, string(up.GetUp().GetOutput()))
		}
	}
	return out
}

func (f *fakeStream) ready() *agentv1.TermReady {
	for _, up := range f.ups() {
		if r := up.GetUp().GetReady(); r != nil {
			return r
		}
	}
	return nil
}

// fakeTmux writes a tmux stand-in: argv recorded, one-shots succeed, and
// attach-session prints a banner then echoes stdin like a live client.
// has/display fail for targets naming "gone", like a missing session.
func fakeTmux(t *testing.T) (program, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\necho \"$@\" >> ")
	sb.WriteString(argvFile)
	sb.WriteString("\ncase \" $* \" in\n" +
		"*gone*)\n" +
		"  echo \"can't find session\" >&2\n" +
		"  exit 1 ;;\n" +
		"esac\n" +
		"has_attach=0\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"attach-session\" ]; then has_attach=1; fi\n" +
		"done\n" +
		"if [ \"$has_attach\" = \"1\" ]; then\n" +
		"  echo \"BANNER\"\n" +
		"  exec cat\n" +
		"fi\n" +
		"case \" $* \" in\n" +
		"*\" display-message \"*) echo \"testgroup\" ;;\n" +
		"*\" list-windows \"*) echo \"@7 0\" ;;\n" +
		"*\" list-panes \"*) echo \"%9 0\" ;;\n" +
		"*\" list-clients \"*) echo \"fakeclient\" ;;\n" +
		"esac\n" +
		"exit 0\n")
	script := sb.String()
	program = filepath.Join(dir, "tmux")
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil { //nolint:gosec // G306: the fake tmux must execute
		t.Fatal(err)
	}
	return program, argvFile
}

func argvLines(t *testing.T, argvFile string) []string {
	t.Helper()
	raw, err := os.ReadFile(argvFile) //nolint:gosec // G304: the test's own argv file
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// testManager builds a manager against the fake tmux and a fake dial.
func testManager(t *testing.T, program string, streams map[string]*fakeStream) *Manager {
	t.Helper()
	caller, err := proc.For("tmux")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(context.Background(), Options{
		Program: program, Runner: runner, Caller: caller,
		ServerStart: func(string) (string, bool) { return "start-1", true },
		Sockets: func(k typev1.TmuxServerKind) (string, string, bool) {
			if k == typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT {
				return "/fake/socket", "default", true
			}
			return "", "", false
		},
		Dial: func(ctx context.Context) (Stream, error) {
			st := &fakeStream{recv: make(chan *agentv1.TerminalResponse, 16), dialCtx: ctx}
			id, _ := ctx.Value(streamKey{}).(string)
			streams[id] = st
			return st, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type streamKey struct{}

func openCmd(id, name string, cols, rows uint32, readOnly bool) *agentv1.OpenAttach {
	return &agentv1.OpenAttach{
		AttachId: &typev1.AttachId{Value: id},
		Session: &agentv1.SessionTarget{
			Server: typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT,
			By:     &agentv1.SessionTarget_Name{Name: name},
		},
		Cols: cols, Rows: rows, ReadOnly: readOnly,
	}
}

func down(id string, set func(*agentv1.TermDown)) *agentv1.TerminalResponse {
	d := &agentv1.TermDown{AttachId: &typev1.AttachId{Value: id}}
	set(d)
	return &agentv1.TerminalResponse{Down: d}
}

func withInput(data []byte) func(*agentv1.TermDown) {
	return func(d *agentv1.TermDown) { d.Body = &agentv1.TermDown_Input{Input: data} }
}

// waitFor polls cond until true or the timeout, failing the test after.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func joinedOutputs(st *fakeStream) string { return strings.Join(st.outputs(), "") }

// TestManagerOpenRoundTrip: open returns the view, the client attaches with
// the browser's size, ready names it, and keystrokes round-trip.
func TestManagerOpenRoundTrip(t *testing.T) {
	t.Parallel()
	program, argvFile := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	ctx := context.WithValue(context.Background(), streamKey{}, "a1")
	view, err := m.Open(ctx, openCmd("a1", "work", 80, 24, false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if want := "fleet-view-a1"; view != want {
		t.Fatalf("view = %q, want %q", view, want)
	}
	st := streams["a1"]
	waitFor(t, "ready", 5*time.Second, func() bool { return st.ready() != nil })
	ready := st.ready()
	if ready.GetCols() != 80 || ready.GetRows() != 24 || ready.GetViewSessionName() != view {
		t.Fatalf("ready = %+v, want 80x24 %q", ready, view)
	}
	// The client attached for real: banner output flows stripped.
	waitFor(t, "banner", 5*time.Second, func() bool {
		return strings.Contains(joinedOutputs(st), "BANNER")
	})
	// Keystrokes round-trip through the PTY echo.
	st.recv <- down("a1", withInput([]byte("MARK-1\n")))
	waitFor(t, "echo", 5*time.Second, func() bool {
		return strings.Contains(joinedOutputs(st), "MARK-1")
	})
	// Resize, window and pane selects are accepted and leave the attach up.
	st.recv <- down("a1", func(d *agentv1.TermDown) {
		d.Body = &agentv1.TermDown_Resize{Resize: &agentv1.TermResize{Cols: 100, Rows: 30}}
	})
	st.recv <- down("a1", func(d *agentv1.TermDown) {
		d.Body = &agentv1.TermDown_Select{Select: &agentv1.TermSelect{
			Target: &agentv1.TermSelect_Window{Window: &typev1.WindowKey{Server: "default", ServerStartId: "start-1", WindowId: "@7"}},
		}}
	})
	st.recv <- down("a1", func(d *agentv1.TermDown) {
		d.Body = &agentv1.TermDown_Select{Select: &agentv1.TermSelect{
			Target: &agentv1.TermSelect_Pane{Pane: &typev1.PaneKey{Server: "default", ServerStartId: "start-1", PaneId: "%9"}},
		}}
	})
	st.recv <- down("a1", withInput([]byte("MARK-2\n")))
	waitFor(t, "post-select echo", 5*time.Second, func() bool {
		return strings.Contains(joinedOutputs(st), "MARK-2")
	})
	// Operator attach: no read-only flag on the client.
	for _, line := range argvLines(t, argvFile) {
		if strings.Contains(line, "attach-session") && strings.Contains(line, "read-only") {
			t.Fatalf("operator argv = %q, want no read-only flag", line)
		}
	}
	if ids := m.OpenIDs(); len(ids) != 1 || ids[0] != "a1" {
		t.Fatalf("OpenIDs = %q, want [a1]", ids)
	}
	m.DropAll()
}

// TestManagerOpenReadOnlyDropsInput: a viewer still renders output, but
// typed input never reaches the client, and tmux itself is told read-only.
func TestManagerOpenReadOnlyDropsInput(t *testing.T) {
	t.Parallel()
	program, argvFile := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	ctx := context.WithValue(context.Background(), streamKey{}, "v9")
	if _, err := m.Open(ctx, openCmd("v9", "work", 80, 24, true)); err != nil {
		t.Fatalf("Open: %v", err)
	}
	st := streams["v9"]
	waitFor(t, "ready", 5*time.Second, func() bool { return st.ready() != nil })
	waitFor(t, "banner", 5*time.Second, func() bool {
		return strings.Contains(joinedOutputs(st), "BANNER")
	})
	st.recv <- down("v9", withInput([]byte("SNEAKY\n")))
	time.Sleep(300 * time.Millisecond)
	if got := joinedOutputs(st); strings.Contains(got, "SNEAKY") {
		t.Fatalf("viewer input reached the client: %q", got)
	}
	found := false
	for _, line := range argvLines(t, argvFile) {
		if strings.Contains(line, "attach-session") && strings.Contains(line, "-f read-only") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no attach-session -f read-only in %q", argvLines(t, argvFile))
	}
}

// TestManagerOpenRefusals: bad ids, sizes, servers and missing targets fail
// closed with the code the handler maps, and create nothing.
func TestManagerOpenRefusals(t *testing.T) {
	t.Parallel()
	program, _ := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	badName := openCmd("ok1", "work", 80, 24, false)
	badName.Session = &agentv1.SessionTarget{
		Server: typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT,
		By:     &agentv1.SessionTarget_TmuxId{TmuxId: "$3"},
	}
	probe := openCmd("ok1", "work", 80, 24, false)
	probe.Session.Server = typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE
	for _, tc := range []struct {
		name string
		cmd  *agentv1.OpenAttach
		want error
	}{
		{"empty id", openCmd("", "work", 80, 24, false), ErrInvalid},
		{"upper id", openCmd("ABC", "work", 80, 24, false), ErrInvalid},
		{"zero cols", openCmd("c1", "work", 0, 24, false), ErrInvalid},
		{"huge rows", openCmd("c1", "work", 80, 1001, false), ErrInvalid},
		{"nil session", func() *agentv1.OpenAttach { c := openCmd("c1", "work", 80, 24, false); c.Session = nil; return c }(), ErrInvalid},
		{"tmux-id target", badName, ErrInvalid},
		{"unserved server", probe, ErrServerDown},
		{"missing target", openCmd("c1", "gone", 80, 24, false), ErrNoSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), streamKey{}, tc.name)
			if _, err := m.Open(ctx, tc.cmd); !errors.Is(err, tc.want) {
				t.Fatalf("Open = %v, want %v", err, tc.want)
			}
		})
	}
	if ids := m.OpenIDs(); len(ids) != 0 {
		t.Fatalf("OpenIDs = %q, want none after refusals", ids)
	}
}

// TestManagerOpenTwiceReturnsView: re-opening an open attach returns its
// view instead of making a second client.
func TestManagerOpenTwiceReturnsView(t *testing.T) {
	t.Parallel()
	program, argvFile := fakeTmux(t)
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	newSessions := func() int {
		n := 0
		for _, line := range argvLines(t, argvFile) {
			if strings.Contains(line, "new-session") {
				n++
			}
		}
		return n
	}
	ctx := context.WithValue(context.Background(), streamKey{}, "dup")
	first, err := m.Open(ctx, openCmd("dup", "work", 80, 24, false))
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	second, err := m.Open(ctx, openCmd("dup", "work", 80, 24, false))
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if second != first {
		t.Fatalf("second view = %q, want %q", second, first)
	}
	if n := newSessions(); n != 1 {
		t.Fatalf("new-session ran %d times, want 1", n)
	}
}

func TestManagerOpenLandingTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	argvFile := dir + "/argv"
	script := "#!/bin/sh\necho \"$@\" >> " + argvFile + "\n" +
		"case \" $* \" in\n*gone*)\n  echo \"can't find session\" >&2\n  exit 1 ;;\nesac\n" +
		"case \" $* \" in\n*\" display-message \"*) echo \"testgroup\" ;;\n*\" list-clients \"*) exit 0 ;;\nesac\nexit 0\n"
	program := dir + "/tmux"
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil { //nolint:gosec // G306: the fake tmux must execute
		t.Fatal(err)
	}
	streams := map[string]*fakeStream{}
	m := testManager(t, program, streams)
	defer m.DropAll()
	ctx := context.WithValue(context.Background(), streamKey{}, "noland")
	start := time.Now()
	if _, err := m.Open(ctx, openCmd("noland", "work", 80, 24, false)); !errors.Is(err, ErrServerDown) {
		t.Fatalf("Open = %v, want ErrServerDown", err)
	}
	if took := time.Since(start); took > landWait+30*time.Second {
		t.Fatalf("Open took %v, want it bounded", took)
	}
	if ids := m.OpenIDs(); len(ids) != 0 {
		t.Fatalf("OpenIDs = %q, want nothing left behind", ids)
	}
	// The view the failed open made is gone, so nothing blocks a retry.
	for _, line := range argvLines(t, argvFile) {
		if strings.Contains(line, "kill-session") && strings.Contains(line, "fleet-view-noland") {
			return
		}
	}
	t.Fatal("no kill-session for the failed open's view")
}
