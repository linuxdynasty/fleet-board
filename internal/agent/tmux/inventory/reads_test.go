package inventory_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/inventory"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
)

const readsTotal = "fleet_agent_tmux_inventory_reads_total"

// count reads a counter series as a number; a missing series is 0.
func (r *obsRig) count(t *testing.T, name string, labels ...string) int {
	t.Helper()
	v := r.metric(t, name, labels...)
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		t.Fatalf("%s%v = %q, not a count", name, labels, v)
	}
	return n
}

// The observer builds its clients with the tmux parser's Metrics, so a
// text field over the 1 KiB bound shows in production: the field reads as
// blank, fleet_agent_tmux_inventory_fields_dropped_total{field, reason}
// rises and tmux.inventory_field_dropped is logged with the byte count,
// never the value. Each good read counts as result ok.
func TestObserverCountsDroppedFields(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			dev.Run(t, "set-option", "-t", "work", "@fleet_owner", strings.Repeat("q", 2000))
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			r.snapshot(t)

			const name = "fleet_agent_tmux_inventory_fields_dropped_total"
			if n := r.count(t, name, `field="fleet_owner"`, `reason="over_bound"`); n < 1 {
				t.Errorf("%s{field=fleet_owner,reason=over_bound} = %d, want at least 1", name, n)
			}
			if n := r.count(t, readsTotal, `server_kind="default"`, `result="ok"`); n < 1 {
				t.Errorf("%s{server_kind=default,result=ok} = %d, want at least 1", readsTotal, n)
			}
			lines := r.logs.named("tmux.inventory_field_dropped")
			if len(lines) == 0 {
				t.Fatalf("no tmux.inventory_field_dropped line:\n%s", r.logs.String())
			}
			if e := lines[0]; e["field"] != "fleet_owner" || e["reason"] != "over_bound" || e["bytes"] != float64(2000) {
				t.Errorf("tmux.inventory_field_dropped = %v, want field fleet_owner, reason over_bound, bytes 2000", e)
			}
			if strings.Contains(r.logs.String(), "qqqq") || strings.Contains(r.scrape(t), "qqqq") {
				t.Error("the value reached a log line or the scrape")
			}
		})
	}
}

// A read that fails is counted by its result, and what the hub is told
// stays the last good inventory with observed true (the control client is
// still attached: snapshot.proto TmuxServer.observed): nothing is reported
// gone or closed. The next good read counts ok again.
func TestObserverCountsFailedReads(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			dev := folder.Sibling(t, "default", "-s", "work", "exec cat")
			// One pane's record (about 700 bytes) fits; with two 1,000-byte
			// tags it does not, so the reply is cut and the read fails.
			r := startObserver(t, b, folder.Dir, func(o *inventory.Options) { inventory.SetReplyBoundForTest(o, 2048) }, "default")
			r.waitState(t, "default", tmux.StateUp)
			good := r.snapshot(t).GetTmuxServers()
			if len(good) != 1 || !good[0].GetObserved() || !good[0].GetRunning() || len(good[0].GetSessions()) != 1 {
				t.Fatalf("first snapshot = %v, want one observed running server with one session", good)
			}
			okBefore := r.count(t, readsTotal, `server_kind="default"`, `result="ok"`)
			if okBefore < 1 {
				t.Fatalf("%s{result=ok} = %d before the failures, want at least 1", readsTotal, okBefore)
			}

			big := strings.Repeat("x", 1000)
			dev.Run(t, "set-option", "-t", "work", "@fleet_owner", big)
			dev.Run(t, "set-option", "-t", "work", "@fleet_goal_id", big)
			for i := range 3 {
				srv := r.snapshot(t).GetTmuxServers()
				if len(srv) != 1 || !srv[0].GetObserved() || !srv[0].GetRunning() || len(srv[0].GetSessions()) != 1 ||
					srv[0].GetServerStartId() != good[0].GetServerStartId() {
					t.Fatalf("snapshot %d after a failed read = %v, want the last good inventory, observed", i, srv)
				}
			}
			if n := r.count(t, readsTotal, `server_kind="default"`, `result="reply"`); n < 3 {
				t.Errorf("%s{server_kind=default,result=reply} = %d, want at least 3", readsTotal, n)
			}
			// The parser's own count of the cut replies: its Metrics is wired too.
			if n := r.count(t, "fleet_agent_tmux_parse_errors_total", `err_kind="reply_too_large"`); n < 3 {
				t.Errorf("fleet_agent_tmux_parse_errors_total{err_kind=reply_too_large} = %d, want at least 3", n)
			}
			failed := r.logs.named("tmux.inventory_failed")
			if len(failed) != 1 || failed[0]["server_kind"] != "default" || failed[0]["err_kind"] != "reply" {
				t.Errorf("tmux.inventory_failed lines = %v, want one (once a minute) with server_kind default, err_kind reply", failed)
			}
			for _, ev := range r.rec.all() {
				if serverKind(ev, agentv1.ChangeKind_CHANGE_KIND_CLOSED) || ev.GetPaneExited() != nil || ev.GetSession().GetKind() == agentv1.ChangeKind_CHANGE_KIND_CLOSED {
					t.Errorf("a failed read reported something gone: %v", ev)
				}
			}

			dev.Run(t, "set-option", "-u", "-t", "work", "@fleet_owner")
			dev.Run(t, "set-option", "-u", "-t", "work", "@fleet_goal_id")
			if srv := r.snapshot(t).GetTmuxServers(); len(srv) != 1 || !srv[0].GetObserved() || len(srv[0].GetSessions()) != 1 {
				t.Fatalf("snapshot after the tags went = %v", srv)
			}
			if n := r.count(t, readsTotal, `server_kind="default"`, `result="ok"`); n <= okBefore {
				t.Errorf("%s{result=ok} = %d after a good read, want more than %d", readsTotal, n, okBefore)
			}
		})
	}
}

// Each inventory read is bounded at 10s in the observer: a read past it is
// abandoned and counted as result timeout, keeping the last good inventory
// with observed true and emitting nothing gone. The log is throttled to one
// line a minute like every other read failure.
func TestObserverReadTimeout(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			folder.Sibling(t, "default", "-s", "work", "exec cat")
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			good := r.snapshot(t).GetTmuxServers()
			if len(good) != 1 || !good[0].GetObserved() || !good[0].GetRunning() || len(good[0].GetSessions()) != 1 {
				t.Fatalf("first snapshot = %v, want one observed running server with one session", good)
			}
			okBefore := r.count(t, readsTotal, `server_kind="default"`, `result="ok"`)
			evBefore := len(r.rec.all())

			restore := inventory.SetReadTimeoutForTest(time.Nanosecond)
			defer restore()
			for i := range 3 {
				srv := r.snapshot(t).GetTmuxServers()
				if len(srv) != 1 || !srv[0].GetObserved() || !srv[0].GetRunning() || len(srv[0].GetSessions()) != 1 ||
					srv[0].GetServerStartId() != good[0].GetServerStartId() {
					t.Fatalf("snapshot %d after a timed-out read = %v, want the last good inventory, observed", i, srv)
				}
			}
			if n := r.count(t, readsTotal, `server_kind="default"`, `result="timeout"`); n < 3 {
				t.Errorf("%s{server_kind=default,result=timeout} = %d, want at least 3", readsTotal, n)
			}
			failed := r.logs.named("tmux.inventory_failed")
			if len(failed) != 1 || failed[0]["server_kind"] != "default" || failed[0]["err_kind"] != "timeout" {
				t.Errorf("tmux.inventory_failed lines = %v, want one (once a minute) with server_kind default, err_kind timeout", failed)
			}
			for _, ev := range r.rec.all()[evBefore:] {
				if serverKind(ev, agentv1.ChangeKind_CHANGE_KIND_CLOSED) || ev.GetPaneExited() != nil || ev.GetSession().GetKind() == agentv1.ChangeKind_CHANGE_KIND_CLOSED {
					t.Errorf("a timed-out read reported something gone: %v", ev)
				}
			}

			restore()
			if srv := r.snapshot(t).GetTmuxServers(); len(srv) != 1 || !srv[0].GetObserved() || len(srv[0].GetSessions()) != 1 {
				t.Fatalf("snapshot after the timeout went = %v", srv)
			}
			if n := r.count(t, readsTotal, `server_kind="default"`, `result="ok"`); n <= okBefore {
				t.Errorf("%s{result=ok} = %d after a good read, want more than %d", readsTotal, n, okBefore)
			}
		})
	}
}

// A server whose every read has timed out since the agent started has no
// last inventory, so it is reported running false, like one never seen.
func TestObserverTimeoutWithNoGoodReadYet(t *testing.T) {
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			folder := tmuxtest.Start(t, b, "-s", "unwatched", "exec cat")
			folder.Sibling(t, "default", "-s", "work", "exec cat")
			restore := inventory.SetReadTimeoutForTest(time.Nanosecond)
			defer restore()
			r := startObserver(t, b, folder.Dir, nil, "default")
			r.waitState(t, "default", tmux.StateUp)
			srv := r.snapshot(t).GetTmuxServers()
			if len(srv) != 1 || srv[0].GetRunning() || srv[0].GetObserved() || len(srv[0].GetSessions()) != 0 {
				t.Fatalf("snapshot with every read timed out = %v, want one server running false, observed false, no sessions", srv)
			}
			if n := r.count(t, readsTotal, `server_kind="default"`, `result="timeout"`); n < 1 {
				t.Errorf("%s{result=timeout} = %d, want at least 1", readsTotal, n)
			}
		})
	}
}
