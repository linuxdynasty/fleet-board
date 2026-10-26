package control

import (
	"context"
	"errors"
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// recRunner answers one canned tmux stdout per subcommand and records
// every argv, so window-start tests pin the exact invocations. A
// missing subcommand fails, so a test that reaches an unscripted call
// fails instead of passing on silence.
type recRunner struct {
	out   map[string]string
	err   map[string]error
	calls [][]string
}

func (s *recRunner) Run(_ context.Context, sp proc.Spec) (proc.Result, error) {
	argv := append([]string{}, sp.Args...)
	s.calls = append(s.calls, argv)
	sub := ""
	if len(sp.Args) > 2 {
		sub = sp.Args[2]
	}
	if err, ok := s.err[sub]; ok {
		return proc.Result{}, err
	}
	if _, ok := s.out[sub]; !ok {
		return proc.Result{}, errors.New("control test: no script for " + sub)
	}
	return proc.Result{Stdout: []byte(s.out[sub])}, nil
}

func goalOptions(r Runner, roots []string) Options {
	o := testOptions()
	o.Runner = r
	o.Roots = func() []string { return roots }
	o.LookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	return o
}

func goalArgs(dir string) GoalWindowArgs {
	return GoalWindowArgs{
		Window: "claude eshu#1", Dir: dir, Harness: "claude", GoalID: "goal1",
	}
}

func TestStartGoalWindowNewSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := &recRunner{out: map[string]string{
		"list-sessions":   "",
		"new-session":     "",
		"display-message": "1234 999",
		"set-option":      "",
		"set-environment": "",
		"list-windows":    "",
		"new-window":      "@1 %1 0",
		"show-options":    "",
	}, err: map[string]error{
		"has-session": errors.New("tmux has-session: can't find session: test"),
	}}
	e, err := New(goalOptions(r, []string{dir}))
	if err != nil {
		t.Fatal(err)
	}
	args := goalArgs(dir)
	args.SessionName = "sess1"
	got, res := e.StartGoalWindow(context.Background(), args)
	if res != nil {
		t.Fatalf("StartGoalWindow = %+v", res)
	}
	if got.Pane.GetPaneId() != "%1" || got.SessionName != "sess1" || !got.CreatedWindow {
		t.Fatalf("window = %+v", got)
	}
	var sawEnv, sawBin bool
	for _, c := range r.calls {
		for i, a := range c {
			if a == "-e" && i+1 < len(c) && c[i+1] == "FLEET_GOAL_ID=goal1" {
				sawEnv = true
			}
			if a == "/usr/bin/claude" {
				sawBin = true
			}
		}
	}
	if !sawEnv {
		t.Fatalf("no FLEET_GOAL_ID in %+v", r.calls)
	}
	if !sawBin {
		t.Fatalf("no harness binary in %+v", r.calls)
	}
}

func TestStartGoalWindowResumeByGoalTag(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := &recRunner{out: map[string]string{
		"list-sessions":   "sess1\n",
		"list-windows":    "@1 0 claude\n",
		"show-options":    "goal1",
		"list-panes":      "%5",
		"display-message": "1234 999",
	}}
	e, err := New(goalOptions(r, []string{dir}))
	if err != nil {
		t.Fatal(err)
	}
	args := goalArgs(dir)
	args.SessionName = "sess1"
	got, res := e.StartGoalWindow(context.Background(), args)
	if res != nil {
		t.Fatalf("StartGoalWindow = %+v", res)
	}
	if got.Pane.GetPaneId() != "%5" || got.CreatedWindow {
		t.Fatalf("window = %+v; want reattach to %%5", got)
	}
	for _, c := range r.calls {
		if c[0] == "new-window" {
			t.Fatalf("resumed run opened a second window: %+v", r.calls)
		}
	}
}

func TestStartGoalWindowRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*GoalWindowArgs)
		roots  []string
		reason typev1.ReasonCode
	}{
		{
			name: "profile refused", mutate: func(a *GoalWindowArgs) { a.Profile = "work" },
			reason: typev1.ReasonCode_REASON_CODE_HARNESS_PROFILE_MISSING,
		},
		{
			name: "bad window name", mutate: func(a *GoalWindowArgs) { a.Window = "has\nnewline" },
			reason: typev1.ReasonCode_REASON_CODE_INVALID_NAME,
		},
		{
			name: "opencode has no binary", mutate: func(a *GoalWindowArgs) { a.Harness = "opencode" },
			reason: typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED,
		},
		{
			name: "dir outside roots", mutate: func(a *GoalWindowArgs) { a.Dir = "/else/where" },
			reason: typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			r := &recRunner{out: map[string]string{}, err: map[string]error{}}
			e, err := New(goalOptions(r, []string{dir}))
			if err != nil {
				t.Fatal(err)
			}
			args := goalArgs(dir)
			tc.mutate(&args)
			if _, res := e.StartGoalWindow(context.Background(), args); res == nil ||
				res.GetReason() != tc.reason {
				t.Fatalf("res = %+v; want %v", res, tc.reason)
			}
			if len(r.calls) != 0 {
				t.Fatalf("typed into tmux after a refusal: %+v", r.calls)
			}
		})
	}
}

func TestStartGoalWindowMissingBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r := &recRunner{out: map[string]string{}, err: map[string]error{}}
	o := goalOptions(r, []string{dir})
	o.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	e, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if _, res := e.StartGoalWindow(context.Background(), goalArgs(dir)); res == nil ||
		res.GetReason() != typev1.ReasonCode_REASON_CODE_HARNESS_NOT_INSTALLED {
		t.Fatalf("res = %+v", res)
	}
}

func TestStartPipeCapture(t *testing.T) {
	t.Parallel()
	r := &recRunner{out: map[string]string{"pipe-pane": ""}}
	e, err := New(goalOptions(r, []string{"/tmp"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartPipeCapture(context.Background(), "%1", "/tmp/state/raw/goal-1.log"); err != nil {
		t.Fatal(err)
	}
	got := r.calls[0]
	want := []string{"pipe-pane", "-o", "-t", "%1", "cat >> '/tmp/state/raw/goal-1.log'"}
	if len(got) != len(want)+2 { // plus -S socket
		t.Fatalf("argv = %q", got)
	}
	for i, w := range want {
		if got[i+2] != w {
			t.Fatalf("argv = %q; want %q", got, want)
		}
	}
	if err := e.StartPipeCapture(context.Background(), "1", "/tmp/x"); err == nil {
		t.Fatal("bad pane id accepted")
	}
	if err := e.StartPipeCapture(context.Background(), "%1", "relative/path"); err == nil {
		t.Fatal("relative capture file accepted")
	}
	// A quote in the path is escaped, never a second command.
	r2 := &recRunner{out: map[string]string{"pipe-pane": ""}}
	e2, err := New(goalOptions(r2, []string{"/tmp"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := e2.StartPipeCapture(context.Background(), "%1", "/tmp/o'clock/x.log"); err != nil {
		t.Fatal(err)
	}
	last := r2.calls[0][len(r2.calls[0])-1]
	if last != "cat >> '/tmp/o'\\''clock/x.log'" {
		t.Fatalf("quoted = %q", last)
	}
}
