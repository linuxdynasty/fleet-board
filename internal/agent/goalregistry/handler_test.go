package goalregistry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
)

func testServices(em *emitter, files goalfile.Writer, workdir string) *Services {
	info := PaneInfo(func(p *typev1.PaneKey) (string, string, error) { return workdir, "claude", nil })
	return NewServices(em.emit, nil, files, info)
}

func registerCmd(goal string, p *typev1.PaneKey, driver bool) *agentv1.Command {
	return &agentv1.Command{Kind: &agentv1.Command_RegisterGoal{RegisterGoal: &agentv1.RegisterGoal{
		Goal: &typev1.GoalId{Value: goal}, Pane: p, RepoDriver: driver,
	}}}
}

func releaseCmd(goal string, p *typev1.PaneKey) *agentv1.Command {
	return &agentv1.Command{Kind: &agentv1.Command_ReleaseGoal{ReleaseGoal: &agentv1.ReleaseGoal{
		Goal: &typev1.GoalId{Value: goal}, Pane: p,
	}}}
}

func TestRegisterHandler(t *testing.T) {
	t.Parallel()
	em := &emitter{}
	svcs := testServices(em, goalfile.Writer{EnsureBoard: stubBoardFor(t, "")}, "")
	h := svcs.RegisterHandler()
	if h.Kind() != "RegisterGoal" {
		t.Fatalf("Kind = %s", h.Kind())
	}
	if h.Class(nil) != exec.RepeatSafe {
		t.Fatalf("Class is not repeat_safe")
	}
	caps := h.Capabilities()
	if len(caps) != 1 || caps[0].GetName() != "goal.registry" || caps[0].GetVersion() != 1 {
		t.Fatalf("Capabilities = %+v", caps)
	}
	res := h.Run(context.Background(), exec.Request{Command: registerCmd("01aaa", pane("default", "s1", "%1"), false)})
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := svcs.Registry().ByPane(pane("default", "s1", "%1")); !ok {
		t.Fatalf("entry missing after RegisterGoal")
	}
	// Bad pane is INVALID_ARGUMENT and binds nothing.
	res = h.Run(context.Background(), exec.Request{Command: registerCmd("01aaa", nil, false)})
	if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
		t.Fatalf("bad pane result = %+v", res)
	}
	// Another kind is INTERNAL.
	res = h.Run(context.Background(), exec.Request{Command: releaseCmd("01aaa", pane("default", "s1", "%1"))})
	if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("wrong kind result = %+v", res)
	}
}

func TestReleaseHandler(t *testing.T) {
	t.Parallel()
	em := &emitter{}
	workdir := t.TempDir()
	files := goalfile.Writer{EnsureBoard: stubBoardFor(t, workdir)}
	svcs := testServices(em, files, workdir)
	h := svcs.ReleaseHandler()
	if h.Kind() != "ReleaseGoal" || h.Class(nil) != exec.RepeatSafe {
		t.Fatalf("Kind/Class = %s", h.Kind())
	}
	p := pane("default", "s1", "%1")
	if res := svcs.RegisterHandler().Run(context.Background(), exec.Request{Command: registerCmd("01aaa", p, false)}); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("register: %+v", res)
	}
	// Plant the goal file the release must remove, plus a neighbor that stays.
	if _, err := files.EnsureGoalFile(workdir, "01aaa", []byte("x\n"), false); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(workdir, ".fleet-board", "notes.txt")
	if err := os.WriteFile(keep, []byte("n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := h.Run(context.Background(), exec.Request{Command: releaseCmd("01aaa", p)})
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("release = %+v", res)
	}
	if _, ok := svcs.Registry().ByPane(p); ok {
		t.Fatalf("entry left behind")
	}
	if _, err := os.Stat(filepath.Join(workdir, ".fleet-board", "goal-01aaa")); !os.IsNotExist(err) {
		t.Fatalf("goal file left behind")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("neighbor file lost: %v", err)
	}
	// Absent entries release OK.
	res = h.Run(context.Background(), exec.Request{Command: releaseCmd("01aaa", p)})
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("absent release = %+v", res)
	}
}

func TestReleaseHandlerUnwiredFiles(t *testing.T) {
	t.Parallel()
	svcs := testServices(&emitter{}, goalfile.Writer{}, t.TempDir())
	p := pane("default", "s1", "%1")
	if res := svcs.RegisterHandler().Run(context.Background(), exec.Request{Command: registerCmd("01aaa", p, false)}); res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("register: %+v", res)
	}
	res := svcs.ReleaseHandler().Run(context.Background(), exec.Request{Command: releaseCmd("01aaa", p)})
	if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_INTERNAL {
		t.Fatalf("unwired files release = %+v, want FAILED/INTERNAL", res)
	}
	if _, ok := svcs.Registry().ByPane(p); ok {
		t.Fatalf("entry left behind after partial release")
	}
}
