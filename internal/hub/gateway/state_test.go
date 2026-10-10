package gateway_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/gateway"
)

// beatCounter follows the hook that reports each Heartbeat wake the
// stream's handler finished (its store job, if any, committed) with the
// stream's Heartbeat count then. One stream per hub in these tests.
type beatCounter struct {
	done chan uint64
	sent uint64
}

func beats() (*beatCounter, func(*gateway.ServerOptions)) {
	b := &beatCounter{done: make(chan uint64, 256)}
	return b, func(o *gateway.ServerOptions) { gateway.SetAfterHeartbeat(o, func(n uint64) { b.done <- n }) }
}

// waitBeat counts one more Heartbeat sent and waits until the hub has
// finished with it.
func waitBeat(t *testing.T, b *beatCounter) {
	t.Helper()
	b.sent++
	end := time.After(10 * time.Second)
	for {
		select {
		case n := <-b.done:
			if n >= b.sent {
				return
			}
		case <-end:
			t.Fatal("the hub did not finish a Heartbeat in 10 s")
		}
	}
}

// 03n, defaults.md: no Heartbeat for 45 s is STALE, 3 minutes OFFLINE, by
// the hub clock, measured from the Hello while no Heartbeat came. A silent
// stream whose reader is not stalled is not cut at STALE; at OFFLINE it is
// cut and its row closed as offline. Each change is logged and counted.
func TestSilenceMarksAMachineStaleThenOffline(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	s := h.online(t, m)
	h.housekeep(t, 44*time.Second)
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Fatalf("state %s at 44 s, want ONLINE", st)
	}
	h.housekeep(t, time.Second)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Fatalf("state %s at 45 s, want STALE", st)
	}
	body := scrape(t, h.fixture)
	if sample(body, "fleet_hub_machine_state", `machine="`+m.id+`"`, `state="STALE"`) != 1 ||
		sample(body, "fleet_hub_machine_state", `machine="`+m.id+`"`, `state="ONLINE"`) != 0 {
		t.Errorf("fleet_hub_machine_state is not STALE only:\n%s", body)
	}
	s.silent(t, 100*time.Millisecond)
	h.housekeep(t, 3*time.Minute-45*time.Second-time.Millisecond)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Fatalf("state %s just under 3 min, want STALE", st)
	}
	h.housekeep(t, time.Millisecond)
	if err := s.ended(t); err != nil {
		t.Errorf("the cut stream ended with %v, want a plain close (no Bye)", err)
	}
	h.waitState(t, m.id, "OFFLINE")
	if got := h.conns(t, m.id); len(got) != 1 || got[0].endReason != "offline" {
		t.Errorf("rows %+v, want epoch 1 closed as offline", got)
	}
	changes := h.stateChanges(t, m.id)
	if len(changes) != 2 || changes[0]["from"] != "ONLINE" || changes[0]["to"] != "STALE" || changes[0]["reason"] != "silence" ||
		changes[1]["from"] != "STALE" || changes[1]["to"] != "OFFLINE" || changes[1]["reason"] != "silence" {
		t.Errorf("machine.state_changed lines %v", changes)
	}
	closed := h.events(t, "hub.stream_closed")
	if len(closed) != 1 || closed[0]["machine_id"] != m.id || closed[0]["epoch"] != float64(1) || closed[0]["reason"] != "offline" ||
		closed[0]["stalled_s"] != float64(0) || closed[0]["level"] != "warn" {
		t.Errorf("hub.stream_closed lines %v", closed)
	}
	h.waitMetric(t, "fleet_hub_stream_closed_total", 1, `reason="offline"`)
	h.waitMetric(t, "fleet_hub_machine_state", 1, `machine="`+m.id+`"`, `state="OFFLINE"`)
}

// A Heartbeat keeps the machine ONLINE, and one on the live stream brings
// a STALE machine back to ONLINE (03n), in one writer job.
func TestAHeartbeatBringsAStaleMachineBack(t *testing.T) {
	t.Parallel()
	done, opt := beats()
	h := startHub(t, opt)
	m := h.enrollMachine(t)
	s := h.online(t, m)
	h.clock.set(h.clock.now().Add(30 * time.Second))
	s.send(t, heartbeat())
	waitBeat(t, done)
	h.housekeep(t, 44*time.Second) // 74 s after the Hello, 44 s after the Heartbeat
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Fatalf("state %s 44 s after a Heartbeat, want ONLINE", st)
	}
	h.housekeep(t, time.Second)
	if st, _ := h.machineState(t, m.id); st != "STALE" {
		t.Fatalf("state %s 45 s after a Heartbeat, want STALE", st)
	}
	jobs := h.writerJobs(t)
	s.send(t, heartbeat())
	waitBeat(t, done)
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Fatalf("state %s after a Heartbeat, want ONLINE", st)
	}
	if got := h.writerJobs(t) - jobs; got != 1 {
		t.Errorf("%v writer jobs for the return to ONLINE, want 1", got)
	}
	changes := h.stateChanges(t, m.id)
	if len(changes) != 2 || changes[1]["from"] != "STALE" || changes[1]["to"] != "ONLINE" || changes[1]["reason"] != "heartbeat" {
		t.Errorf("machine.state_changed lines %v", changes)
	}
	s.silent(t, 100*time.Millisecond)
}

// defaults.md: last_seen is written at most once a minute from Heartbeats
// (and on connect and disconnect); a Heartbeat that changes nothing else
// writes nothing.
func TestLastSeenIsWrittenAtMostOnceAMinute(t *testing.T) {
	t.Parallel()
	done, opt := beats()
	h := startHub(t, opt)
	m := h.enrollMachine(t)
	s := h.online(t, m)
	_, hello := h.machineState(t, m.id)
	jobs := h.writerJobs(t)
	for range 5 { // +10 s .. +50 s
		h.clock.set(h.clock.now().Add(10 * time.Second))
		s.send(t, heartbeat())
		waitBeat(t, done)
	}
	if _, seen := h.machineState(t, m.id); seen != hello {
		t.Errorf("last_seen %v moved within the minute, want %v", seen, hello)
	}
	if got := h.writerJobs(t) - jobs; got != 0 {
		t.Errorf("%v writer jobs for Heartbeats within a minute, want 0", got)
	}
	h.clock.set(h.clock.now().Add(10 * time.Second)) // +60 s
	s.send(t, heartbeat())
	waitBeat(t, done)
	at := h.clock.now().UnixMilli()
	if _, seen := h.machineState(t, m.id); seen.Int64 != at {
		t.Errorf("last_seen %v after a minute, want %d", seen, at)
	}
	h.clock.set(h.clock.now().Add(30 * time.Second))
	s.send(t, heartbeat())
	waitBeat(t, done)
	if _, seen := h.machineState(t, m.id); seen.Int64 != at {
		t.Errorf("last_seen %v 30 s after the write, want %d", seen, at)
	}
	if got := h.writerJobs(t) - jobs; got != 1 {
		t.Errorf("%v writer jobs, want 1 (the minute's last_seen)", got)
	}
}

// #138, 03f race table: a displaced stream that closes after the newer
// Hello leaves the machine ONLINE; the live stream's close still takes it
// OFFLINE at once.
func TestADisplacedStreamsCloseLeavesTheMachineOnline(t *testing.T) {
	t.Parallel()
	h := startHub(t)
	m := h.enrollMachine(t)
	first := h.online(t, m)
	h.clock.set(h.clock.now().Add(gateway.HelloEvery))
	second := h.open(t, m)
	second.send(t, hello(m, 1))
	second.ack(t)
	if got := first.bye(t); got.String() != "BYE_REASON_DUPLICATE_IDENTITY" {
		t.Fatalf("old stream's Bye %v", got)
	}
	h.waitConns(t, m.id, "epoch 1 displaced", func(c []conn) bool { return len(c) == 2 && c[0].endReason == "displaced" })
	h.waitClosed(t, 1)
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Fatalf("state %s after the displaced stream closed, want ONLINE", st)
	}
	if n := len(h.stateChanges(t, m.id)); n != 0 {
		t.Errorf("%d machine.state_changed lines for a displaced close, want 0", n)
	}
	_ = second.bs.CloseRequest() // the agent's side ends: the hub's reader sees the end
	h.waitState(t, m.id, "OFFLINE")
	if c := h.waitChanges(t, m.id, 1); len(c) != 1 || c[0]["reason"] != "stream_closed" || c[0]["from"] != "ONLINE" {
		t.Errorf("machine.state_changed lines %v, want one ONLINE to OFFLINE for stream_closed", c)
	}
}

// 02i "Clocks": the skew is agent time minus hub receive time, the median
// of the last five samples, so one slow packet does not move it.
func TestClockSkewIsTheMedianOfTheLastFiveSamples(t *testing.T) {
	t.Parallel()
	done, opt := beats()
	h := startHub(t, opt)
	m := h.enrollMachine(t)
	s := h.online(t, m)
	for _, ahead := range []int{10, 12, 900, 11, 10, 9} {
		s.send(t, sentHeartbeat(h.clock.now().Add(time.Duration(ahead)*time.Second)))
		waitBeat(t, done)
	}
	// The last five: 12, 900, 11, 10, 9 s; the median is 11 s.
	h.waitMetric(t, "fleet_hub_clock_skew_seconds", 11, `machine="`+m.id+`"`)
	s.send(t, heartbeat()) // no sent_time: not a sample
	waitBeat(t, done)
	h.waitMetric(t, "fleet_hub_clock_skew_seconds", 11, `machine="`+m.id+`"`)
}

// #138 when the old stream's own close wins the race: it ends (the agent
// went away) after the newer Hello committed and before the registry
// displaced it, so its close carries stream_closed. Its row is already
// closed as displaced, so the close changes nothing and the machine stays
// ONLINE on the new stream.
func TestAnOldStreamClosingAfterTheNewerHelloCommitsLeavesTheMachineOnline(t *testing.T) {
	t.Parallel()
	var (
		old  atomic.Pointer[agentStream]
		hubp atomic.Pointer[hub]
	)
	h := startHub(t, func(o *gateway.ServerOptions) {
		gateway.SetAfterHelloCommit(o, func() {
			s := old.Swap(nil)
			if s == nil {
				return // the first Hello
			}
			_ = s.bs.CloseRequest()
			// Not t: this runs on the hub's goroutine. The test checks below.
			logs := hubp.Load().logs
			for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
				if strings.Contains(logs.String(), `"msg":"reconnect.closed"`) {
					return
				}
			}
		})
	})
	hubp.Store(h)
	m := h.enrollMachine(t)
	first := h.online(t, m)
	old.Store(first)
	h.clock.set(h.clock.now().Add(gateway.HelloEvery))
	second := h.open(t, m)
	second.send(t, hello(m, 1))
	second.ack(t)
	if l := h.waitClosed(t, 1); l["reason"] != "stream_closed" || l["row_closed"] != false {
		t.Errorf("the old stream's reconnect.closed %v, want stream_closed with no row closed", l)
	}
	if st, _ := h.machineState(t, m.id); st != "ONLINE" {
		t.Errorf("state %s, want ONLINE on the new stream", st)
	}
	if n := len(h.stateChanges(t, m.id)); n != 0 {
		t.Errorf("%d machine.state_changed lines, want 0", n)
	}
}
