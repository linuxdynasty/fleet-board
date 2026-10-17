package goals_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goals"
	"github.com/linuxdynasty/fleet-board/internal/agent/repos"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

type stubPanes struct {
	panes  []goals.Pane
	loc    map[string]loc
	locErr map[string]bool
}

type loc struct {
	path string
	pid  int32
}

func (s *stubPanes) List(context.Context) ([]goals.Pane, error) { return s.panes, nil }
func (s *stubPanes) Locate(_ context.Context, _, id string) (string, int32, error) {
	if s.locErr[id] {
		return "", 0, errLocate
	}
	l := s.loc[id]
	return l.path, l.pid, nil
}

type stubErr2 struct{ msg string }

func (e stubErr2) Error() string { return e.msg }

var errLocate = stubErr2{"no such pane"}

type stubProcs struct {
	harness map[int32]procHarness
	alive   map[uint32]bool
}

type procHarness struct {
	name  string
	start time.Time
}

func (s *stubProcs) HarnessIn(pid int32) (string, time.Time, bool) {
	h, ok := s.harness[pid]
	return h.name, h.start, ok && h.name != ""
}

func (s *stubProcs) Alive(pid uint32) bool { return s.alive[pid] }

type stubCheckouts struct {
	dir     string
	details map[string]repos.Detail
}

func (s *stubCheckouts) Workdir(path string) *typev1.PaneWorkdir {
	top := s.dir
	if path != top && len(path) > len(top)+1 && path[:len(top)+1] == top+"/" {
		return &typev1.PaneWorkdir{CurrentPath: path, CheckoutPath: top}
	}
	if path == top {
		return &typev1.PaneWorkdir{CurrentPath: path, CheckoutPath: top}
	}
	return nil
}

func (s *stubCheckouts) Detail(top string) (repos.Detail, bool) {
	d, ok := s.details[top]
	return d, ok
}

type stubCaptures struct {
	screens map[string]string
	fail    map[string]bool
}

func (s *stubCaptures) Capture(_ context.Context, _, pane string) (string, error) {
	if s.fail[pane] {
		return "", errCapture
	}
	screen, ok := s.screens[pane]
	if !ok {
		return "", errCapture
	}
	return screen, nil
}

type stubErr3 struct{ msg string }

func (e stubErr3) Error() string { return e.msg }

var errCapture = stubErr3{"no such capture"}

type rig struct {
	t          *testing.T
	dir        string
	now        time.Time
	panes      *stubPanes
	procs      *stubProcs
	procsIface goals.Procs
	check      *stubCheckouts
	captures   *stubCaptures
	emitted    []*agentv1.Event
	db         *ring.DB
	tr         *goals.Tracker
}

func paneOf(id string) goals.Pane {
	return goals.Pane{
		Key:     &typev1.PaneKey{Server: "default", ServerStartId: "s1", PaneId: id},
		Session: &typev1.SessionId{Value: "sess-1"},
		ID:      id,
	}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	boot := time.Unix(100000, 0).UTC()
	r := &rig{
		t:     t,
		dir:   dir,
		now:   boot.Add(time.Hour),
		panes: &stubPanes{loc: map[string]loc{}},
		procs: &stubProcs{harness: map[int32]procHarness{}, alive: map[uint32]bool{}},
		check: &stubCheckouts{dir: dir, details: map[string]repos.Detail{
			dir: {Top: dir, Branch: "fleet/12", Head: "abc123", Dirty: false},
		}},
		captures: &stubCaptures{screens: map[string]string{}, fail: map[string]bool{}},
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := ring.Open(context.Background(), stateDir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	r.db = db
	r.procsIface = r.procs
	r.retrack()
	r.addPane("%1", 100, "claude", boot)
	return r
}

// retrack builds a new Tracker over the same database: an agent restart.
func (r *rig) retrack() {
	r.tr = goals.NewTracker(goals.Config{
		MachineID: "m1",
		Panes:     r.panes,
		Checkouts: r.check,
		Procs:     r.procsIface,
		Files:     goals.OSFiles{},
		DB:        r.db,
		Emit:      r,
		Clock:     func() time.Time { return r.now },
		Captures:  r.captures,
	})
}

func (r *rig) Emit(_ context.Context, ev *agentv1.Event) (uint64, error) {
	r.emitted = append(r.emitted, ev)
	return uint64(len(r.emitted)), nil
}

func (r *rig) addPane(id string, pid int32, harness string, start time.Time) {
	r.t.Helper()
	r.panes.panes = append(r.panes.panes, paneOf(id))
	r.panes.loc[id] = loc{path: r.dir, pid: pid}
	r.procs.harness[pid] = procHarness{name: harness, start: start}
}

func (r *rig) writeGoal(data string, mod time.Time) string {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Join(r.dir, ".claude"), 0o700); err != nil {
		r.t.Fatal(err)
	}
	p := filepath.Join(r.dir, ".claude", "active-goal.s1")
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		r.t.Fatal(err)
	}
	return p
}

func (r *rig) scan() {
	r.t.Helper()
	if err := r.tr.Scan(context.Background()); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) goals() []*agentv1.GoalState {
	snap := &agentv1.Snapshot{}
	r.tr.Fill(snap)
	return snap.GetGoals()
}

func (r *rig) assertAllGoalState(t *testing.T) {
	t.Helper()
	for _, ev := range r.emitted {
		if ev.GetGoalState() == nil {
			t.Fatalf("emitted a non-goal event: %v (the tracker never nudges)", ev)
		}
	}
}
