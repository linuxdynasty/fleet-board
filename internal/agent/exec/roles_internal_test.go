package exec

import (
	"os"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// The executor's matrix is 02g's table, row for row and cell for cell, so a
// change to the spec that the code misses fails here.
func TestRoleMatrixMatchesTheSpec(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../../../docs/specs/02g-agent-security.md")
	if err != nil {
		t.Fatal(err)
	}
	var spec [][]string
	in := false
	for line := range strings.Lines(string(b)) {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "| Command (class or variant) |"):
			in = true
		case in && strings.HasPrefix(line, "|---"):
		case in && strings.HasPrefix(line, "|"):
			cells := strings.Split(strings.Trim(line, "|"), " | ")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			spec = append(spec, cells)
		case in:
			in = false
		}
	}
	if len(spec) != len(roleMatrix) {
		t.Fatalf("02g has %d rows, the executor %d", len(spec), len(roleMatrix))
	}
	for i, row := range roleMatrix {
		got := append([]string{row.doc}, row.cells[:]...)
		if strings.Join(got, " | ") != strings.Join(spec[i], " | ") {
			t.Errorf("row %d:\n code %q\n spec %q", i, got, spec[i])
		}
	}
	for _, row := range roleMatrix {
		for _, k := range row.kinds {
			if !isKind(k) {
				t.Errorf("row %q names kind %s, which the proto lacks", row.doc, k)
			}
		}
	}
}

func TestRoleCells(t *testing.T) {
	t.Parallel()
	sendKeys := func(p typev1.SendKeysPurpose) *agentv1.Command {
		return &agentv1.Command{Kind: &agentv1.Command_SendKeys{SendKeys: &agentv1.SendKeys{Purpose: p}}}
	}
	rhc := func(c typev1.CommandClass) *agentv1.Command {
		return &agentv1.Command{Kind: &agentv1.Command_RunHarnessCommand{RunHarnessCommand: &agentv1.RunHarnessCommand{ExpectedClass: c}}}
	}
	for _, tc := range []struct {
		name  string
		c     *agentv1.Command
		role  typev1.Role
		ok    bool
		check func(cell) bool
	}{
		{"viewer captures", &agentv1.Command{Kind: &agentv1.Command_CapturePane{CapturePane: &agentv1.CapturePane{}}}, typev1.Role_ROLE_VIEWER, true, nil},
		{"no role", &agentv1.Command{Kind: &agentv1.Command_CapturePane{CapturePane: &agentv1.CapturePane{}}}, typev1.Role_ROLE_UNSPECIFIED, false, nil},
		{"scheduler operator text", sendKeys(typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_OPERATOR_TEXT), typev1.Role_ROLE_SCHEDULER, false, nil},
		{
			"scheduler nudge is L, F", sendKeys(typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_GOAL_NUDGE), typev1.Role_ROLE_SCHEDULER, true,
			func(c cell) bool { return c.local && c.fleetOnly },
		},
		{"send keys with no purpose", sendKeys(typev1.SendKeysPurpose_SEND_KEYS_PURPOSE_UNSPECIFIED), typev1.Role_ROLE_OWNER, false, nil},
		{
			"operator destructive is L!", rhc(typev1.CommandClass_COMMAND_CLASS_DESTRUCTIVE), typev1.Role_ROLE_OPERATOR, true,
			func(c cell) bool { return c.alwaysLocal },
		},
		{"owner deny", rhc(typev1.CommandClass_COMMAND_CLASS_DENY), typev1.Role_ROLE_OWNER, false, nil},
		{"no class", rhc(typev1.CommandClass_COMMAND_CLASS_UNSPECIFIED), typev1.Role_ROLE_OWNER, false, nil},
		{"scheduler prune", &agentv1.Command{Kind: &agentv1.Command_PruneWorktree{PruneWorktree: &agentv1.PruneWorktree{}}}, typev1.Role_ROLE_SCHEDULER, true, nil},
		{"scheduler prune dirty", &agentv1.Command{Kind: &agentv1.Command_PruneWorktree{PruneWorktree: &agentv1.PruneWorktree{AllowDirty: true}}}, typev1.Role_ROLE_SCHEDULER, false, nil},
		{"viewer attach", &agentv1.Command{Kind: &agentv1.Command_OpenAttach{OpenAttach: &agentv1.OpenAttach{}}}, typev1.Role_ROLE_VIEWER, false, nil},
		{"viewer closes an attach", &agentv1.Command{Kind: &agentv1.Command_CloseAttach{CloseAttach: &agentv1.CloseAttach{}}}, typev1.Role_ROLE_VIEWER, true, nil},
		{"viewer read-only attach", &agentv1.Command{Kind: &agentv1.Command_OpenAttach{OpenAttach: &agentv1.OpenAttach{ReadOnly: true}}}, typev1.Role_ROLE_VIEWER, true, nil},
		{"operator kills a developer window", &agentv1.Command{Kind: &agentv1.Command_KillWindow{KillWindow: &agentv1.KillWindow{}}}, typev1.Role_ROLE_OPERATOR, false, nil},
		{"owner drains", &agentv1.Command{Kind: &agentv1.Command_Drain{Drain: &agentv1.Drain{}}}, typev1.Role_ROLE_OWNER, true, nil},
		{"operator drains", &agentv1.Command{Kind: &agentv1.Command_Drain{Drain: &agentv1.Drain{}}}, typev1.Role_ROLE_OPERATOR, false, nil},
		{"operator ensures scratch", &agentv1.Command{Kind: &agentv1.Command_EnsureScratch{EnsureScratch: &agentv1.EnsureScratch{}}}, typev1.Role_ROLE_OPERATOR, true, nil},
		{"viewer ensures scratch", &agentv1.Command{Kind: &agentv1.Command_EnsureScratch{EnsureScratch: &agentv1.EnsureScratch{}}}, typev1.Role_ROLE_VIEWER, false, nil},
		{"operator collects scratch", &agentv1.Command{Kind: &agentv1.Command_CollectScratch{CollectScratch: &agentv1.CollectScratch{}}}, typev1.Role_ROLE_OPERATOR, false, nil},
		{"scheduler collects scratch", &agentv1.Command{Kind: &agentv1.Command_CollectScratch{CollectScratch: &agentv1.CollectScratch{}}}, typev1.Role_ROLE_SCHEDULER, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.c.RequestedBy = &typev1.Principal{Role: tc.role}
			cl, ok := roleCell(tc.c, kindOf(tc.c), nil)
			if ok != tc.ok || (tc.check != nil && !tc.check(cl)) {
				t.Errorf("cell %+v ok %v, want ok %v", cl, ok, tc.ok)
			}
		})
	}
}

// Safe mode lets only what a VIEWER may ask for run.
func TestObserveOnly(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		c    *agentv1.Command
		want bool
	}{
		{&agentv1.Command{Kind: &agentv1.Command_CapturePane{CapturePane: &agentv1.CapturePane{}}}, true},
		{&agentv1.Command{Kind: &agentv1.Command_RunHarnessCommand{RunHarnessCommand: &agentv1.RunHarnessCommand{ExpectedClass: typev1.CommandClass_COMMAND_CLASS_OBSERVE}}}, true},
		{&agentv1.Command{Kind: &agentv1.Command_RunHarnessCommand{RunHarnessCommand: &agentv1.RunHarnessCommand{ExpectedClass: typev1.CommandClass_COMMAND_CLASS_SESSION}}}, false},
		{&agentv1.Command{Kind: &agentv1.Command_OpenAttach{OpenAttach: &agentv1.OpenAttach{ReadOnly: true}}}, true},
		{&agentv1.Command{Kind: &agentv1.Command_OpenAttach{OpenAttach: &agentv1.OpenAttach{}}}, false},
		{&agentv1.Command{Kind: &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}}, false},
		{&agentv1.Command{}, false},
	} {
		if got := observeOnly(tc.c, kindOf(tc.c), nil); got != tc.want {
			t.Errorf("%s: observe %v, want %v", kindOf(tc.c), got, tc.want)
		}
	}
}

// The rate window frees as the newest commands over the limit age out.
func TestRateWindow(t *testing.T) {
	t.Parallel()
	t0 := hub()
	fresh := func() *rateWindow {
		w := &rateWindow{times: map[string][]time.Time{}}
		for i := range 3 {
			w.add("K", t0.Add(timeSec(i*10)))
		}
		return w
	}
	now := t0.Add(timeSec(20))
	if wait, over := fresh().overCounting("K", now, now, 2); !over || wait != timeSec(50) {
		t.Errorf("over %v wait %v, want over, 50s (the second command at 10s ages out at 70s)", over, wait)
	}
	if _, over := fresh().overCounting("K", now, now, 3); over {
		t.Error("three commands under a limit of 3 are over")
	}
	later := t0.Add(timeSec(65))
	if _, over := fresh().overCounting("K", later, later, 2); over {
		t.Error("the first command did not leave the window after a minute")
	}
	// One inside the window is counted already, not twice, and not added.
	w := fresh()
	if _, over := w.overCounting("K", later, t0.Add(timeSec(10)), 2); over {
		t.Error("an entry in the window was counted twice")
	}
	if n := len(w.recent("K", later)); n != 2 {
		t.Errorf("checking an entry in the window left %d in it, want 2", n)
	}
	// An entry logged before the window is added now and counted once (10 s
	// and 20 s are in the window, so three is over 2), and it stays for the
	// next check.
	w = fresh()
	if _, over := w.overCounting("K", later, t0.Add(-timeSec(30)), 2); !over {
		t.Error("an entry from before the window was not counted")
	}
	if n := len(w.recent("K", later)); n != 3 {
		t.Errorf("checking an entry from before the window left %d in it, want 3", n)
	}
}

func hub() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) }

func timeSec(n int) time.Duration { return time.Duration(n) * time.Second }
