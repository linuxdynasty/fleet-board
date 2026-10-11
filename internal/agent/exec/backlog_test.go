package exec_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

// agentClock is the agent's own clock (received times, the rate window),
// which a test moves by hand.
type agentClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *agentClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *agentClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// backlogRig is a rig whose executor runs on a hand-moved agent clock.
func backlogRig(t *testing.T, limit int) (*rig, *handler, *agentClock) {
	t.Helper()
	h := capturePane("tmux.observe")
	r := newRig(t)
	clock := &agentClock{now: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	ex, err := exec.New(exec.Options{
		DB: r.db, Sender: r.send, Clock: r.clock, Policy: r.pol, Handlers: []exec.Handler{h},
		Telemetry: r.tel, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.ex = ex
	if limit > 0 {
		r.pol.limits["CapturePane"] = limit
	}
	return r, h, clock
}

// logReceived writes a received CapturePane entry as a stopped agent left
// it, logged age ago on the agent clock.
func logReceived(t *testing.T, r *rig, clock *agentClock, id string, seq uint64, age time.Duration) {
	t.Helper()
	c := cmd(id, seq, "tmux.observe")
	body, err := proto.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.db.LogAndApply(context.Background(), ring.Command{
		ID: id, Kind: "CapturePane", Epoch: 1, Seq: seq, Received: clock.Now().Add(-age),
		NotAfter: c.GetNotAfterTime().AsTime(), Body: body,
	}); err != nil {
		t.Fatal(err)
	}
}

// outcomes waits for each id's result and counts OK and RATE_LIMITED; a
// RATE_LIMITED result must say when to retry.
func outcomes(t *testing.T, r *rig, ids []string) (ok, limited int) {
	t.Helper()
	for _, id := range ids {
		res := r.result(t, id)
		switch {
		case res.GetStatus() == typev1.Status_STATUS_OK:
			ok++
		case res.GetReason() == typev1.ReasonCode_REASON_CODE_RATE_LIMITED:
			limited++
			if res.GetRetryAfter().AsDuration() <= 0 {
				t.Errorf("%s: RATE_LIMITED with retry_after %v, want > 0", id, res.GetRetryAfter().AsDuration())
			}
		default:
			t.Errorf("%s: %v", id, res)
		}
	}
	return ok, limited
}

// An entry logged more than a minute ago is not in the seeded window (the
// window starts with no map when the log holds nothing recent); the resume
// pass must still check it without a panic and answer it.
func TestAnOldEntryAloneIsResumedWithoutAPanic(t *testing.T) {
	t.Parallel()
	r, h, clock := backlogRig(t, 0)
	logReceived(t, r, clock, "old-1", 1, 2*time.Minute)
	r.start(t)
	r.ex.OnConnected(context.Background(), 1, r.clock)
	if res := r.result(t, "old-1"); res.GetStatus() != typev1.Status_STATUS_OK || h.runs.Load() != 1 {
		t.Errorf("result %v after %d runs, want OK after one", res, h.runs.Load())
	}
}

// A backlog of entries from before the window is counted one by one as it
// is taken up: at a limit of 2, five old entries give 2 runs and 3
// RATE_LIMITED, and fresh commands after them are refused too.
func TestABacklogIsCountedAsItIsResumed(t *testing.T) {
	t.Parallel()
	r, h, clock := backlogRig(t, 2)
	var ids []string
	for i := range 5 {
		id := fmt.Sprintf("b-%d", i)
		ids = append(ids, id)
		logReceived(t, r, clock, id, uint64(i+1), 2*time.Minute)
	}
	r.start(t)
	r.ex.OnConnected(context.Background(), 1, r.clock)
	if ok, limited := outcomes(t, r, ids); ok != 2 || limited != 3 {
		t.Errorf("backlog: %d OK and %d RATE_LIMITED, want 2 and 3", ok, limited)
	}
	r.give(t, cmd("fresh-1", 6, "tmux.observe"))
	r.give(t, cmd("fresh-2", 7, "tmux.observe"))
	if ok, limited := outcomes(t, r, []string{"fresh-1", "fresh-2"}); ok != 0 || limited != 2 {
		t.Errorf("fresh after the backlog: %d OK and %d RATE_LIMITED, want 0 and 2", ok, limited)
	}
	if n := h.runs.Load(); n != 2 {
		t.Errorf("the handler ran %d times, want 2", n)
	}
}

// Two connects more than a minute apart: the window frees in between, so
// each backlog gets its own two runs.
func TestBacklogsAMinuteApartEachGetTheLimit(t *testing.T) {
	t.Parallel()
	r, h, clock := backlogRig(t, 2)
	first := []string{"c1-0", "c1-1", "c1-2"}
	for i, id := range first {
		logReceived(t, r, clock, id, uint64(i+1), 2*time.Minute)
	}
	r.start(t)
	r.ex.OnConnected(context.Background(), 1, r.clock)
	if ok, limited := outcomes(t, r, first); ok != 2 || limited != 1 {
		t.Errorf("first connect: %d OK and %d RATE_LIMITED, want 2 and 1", ok, limited)
	}
	clock.add(61 * time.Second)
	second := []string{"c2-0", "c2-1", "c2-2"}
	for i, id := range second {
		logReceived(t, r, clock, id, uint64(i+4), 2*time.Minute)
	}
	r.ex.OnConnected(context.Background(), 2, r.clock)
	if ok, limited := outcomes(t, r, second); ok != 2 || limited != 1 {
		t.Errorf("second connect a minute later: %d OK and %d RATE_LIMITED, want 2 and 1", ok, limited)
	}
	if n := h.runs.Load(); n != 4 {
		t.Errorf("the handler ran %d times, want 4", n)
	}
}

// Entries inside the window (seeded from the log) and outside it share the
// one count: at a limit of 2, one recent entry and three old ones give 2
// runs and 2 RATE_LIMITED.
func TestRecentAndOldEntriesShareTheCount(t *testing.T) {
	t.Parallel()
	r, h, clock := backlogRig(t, 2)
	logReceived(t, r, clock, "m-0", 1, 30*time.Second)
	ids := []string{"m-0"}
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("m-%d", i)
		ids = append(ids, id)
		logReceived(t, r, clock, id, uint64(i+1), 2*time.Minute)
	}
	r.start(t)
	r.ex.OnConnected(context.Background(), 1, r.clock)
	if ok, limited := outcomes(t, r, ids); ok != 2 || limited != 2 {
		t.Errorf("mix: %d OK and %d RATE_LIMITED, want 2 and 2", ok, limited)
	}
	if n := h.runs.Load(); n != 2 {
		t.Errorf("the handler ran %d times, want 2", n)
	}
}

// failAlways is agent.db whose RecordResult always fails for one id.
type failAlways struct {
	exec.Store
	id string
}

func (f failAlways) RecordResult(ctx context.Context, id string, b []byte, at time.Time) error {
	if id == f.id {
		return errors.New("disk full")
	}
	return f.Store.RecordResult(ctx, id, b, at)
}

// An old entry whose result write keeps failing is taken up at every
// connect, and counted once, not once per connect: at a limit of 3, after
// three connects 5 s apart two fresh commands still run.
func TestAnEntryRetriedAtEachConnectIsCountedOnce(t *testing.T) {
	t.Parallel()
	h := &handler{kind: "Interrupt", caps: []*typev1.Capability{{Name: "tmux.control", Version: 1}}}
	r := newRig(t)
	clock := &agentClock{now: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	ex, err := exec.New(exec.Options{
		DB: failAlways{Store: r.db, id: "stuck"}, Sender: r.send, Clock: r.clock, Policy: r.pol,
		Handlers: []exec.Handler{h}, Telemetry: r.tel, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.ex = ex
	r.pol.limits["Interrupt"] = 3
	stuck := cmd("stuck", 1, "tmux.control")
	stuck.RequestedBy.Role = typev1.Role_ROLE_VIEWER // refused after the rate check, and its write fails
	stuck.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
	body, err := proto.Marshal(stuck)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.db.LogAndApply(context.Background(), ring.Command{
		ID: "stuck", Kind: "Interrupt", Epoch: 1, Seq: 1, Received: clock.Now().Add(-2 * time.Minute),
		NotAfter: stuck.GetNotAfterTime().AsTime(), Body: body,
	}); err != nil {
		t.Fatal(err)
	}
	r.start(t)
	for i := range 3 {
		r.ex.OnConnected(context.Background(), uint64(i+1), r.clock)
		want := fmt.Sprint(i + 1)
		waitFor(t, func() bool { return r.metric(t, "fleet_agent_command_store_failed_total", `step="result"`) == want })
		waitFor(t, func() bool { return r.metric(t, "fleet_agent_commands_running", "") == "0" })
		clock.add(5 * time.Second)
	}
	for i, id := range []string{"f-1", "f-2"} {
		c := cmd(id, uint64(i+2), "tmux.control")
		c.Kind = &agentv1.Command_Interrupt{Interrupt: &agentv1.Interrupt{}}
		r.give(t, c)
		if res := r.result(t, id); res.GetStatus() != typev1.Status_STATUS_OK {
			t.Errorf("%s: %v, want OK (the stuck entry counts once)", id, res)
		}
	}
}

// An entry the agent logged while its clock ran an hour ahead is seeded
// into the window at start. It counts as now, so the window frees within a
// minute of the clock as it reads now, and retry_after is never negative.
func TestAnEntryFromAClockAheadFreesWithinAMinute(t *testing.T) {
	t.Parallel()
	r, h, clock := backlogRig(t, 1)
	ahead := clock.Now().Add(time.Hour)
	if _, _, err := r.db.LogCommand(context.Background(), ring.Command{
		ID: "ahead", Kind: "CapturePane", Epoch: 1, Seq: 1, Received: ahead, NotAfter: ahead,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.db.RecordResult(context.Background(), "ahead", []byte{}, ahead); err != nil {
		t.Fatal(err)
	}
	r.start(t)
	r.give(t, cmd("n-1", 2, "tmux.observe"))
	res := r.result(t, "n-1")
	if d := res.GetRetryAfter().AsDuration(); res.GetReason() != typev1.ReasonCode_REASON_CODE_RATE_LIMITED || d <= 0 || d > time.Minute {
		t.Errorf("right after the start: %v (retry_after %v), want RATE_LIMITED within a minute", res, d)
	}
	clock.add(61 * time.Second)
	r.give(t, cmd("n-2", 3, "tmux.observe"))
	if res := r.result(t, "n-2"); res.GetStatus() != typev1.Status_STATUS_OK || h.runs.Load() != 1 {
		t.Errorf("61 s later: %v after %d runs, want OK", res, h.runs.Load())
	}
}
