//go:build unix

package proc_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/proc"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// lockedBuffer is a log sink the logger and the test can share.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) lines() []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(l.b.Bytes()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func scrape(t *testing.T, tel *telemetry.Telemetry) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	tel.MetricsHandler(nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape: status %d", rec.Code)
	}
	return rec.Body.String()
}

func hasSeries(body, prefix string, want ...string) bool {
	return seriesIs(body, prefix, "1", want...)
}

// seriesIs reports a series whose name starts with prefix, whose labels
// contain every want and whose value is value.
func seriesIs(body, prefix, value string, want ...string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(line, w)
		}
		if ok && strings.HasSuffix(line, " "+value) {
			return true
		}
	}
	return false
}

// Every kill logs its cause and counts it; a run that kills nothing does
// neither.
func TestEveryKillIsLoggedAndCounted(t *testing.T) {
	t.Parallel()
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "boot-1", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	r, err := proc.NewRunner(tel)
	if err != nil {
		t.Fatal(err)
	}
	l := proc.Limits{Time: 20 * time.Second, Output: 4 << 10}

	if _, err := r.Run(t.Context(), child(t, "echo", l, exitCodeEnv+"=0")); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	if _, err := r.Run(t.Context(), child(t, "echo", l, exitCodeEnv+"=3")); err == nil {
		t.Fatal("exit 3 returned no error")
	}
	if got := scrape(t, tel); strings.Contains(got, "fleet_agent_proc_kills_total{") {
		t.Fatalf("a clean exit and a non-zero exit are not kills, but the scrape has a series:\n%s", got)
	}

	short := proc.CallerForTest("test_time", proc.Limits{Time: 200 * time.Millisecond, Output: 4 << 10})
	sp := child(t, "hang", l)
	sp.Caller = short
	if _, err := r.Run(t.Context(), sp); err == nil {
		t.Fatal("time cap: no error")
	}
	sp = child(t, "flood", proc.Limits{Time: 20 * time.Second, Output: 8 << 10})
	sp.Caller = proc.CallerForTest("test_output", proc.Limits{Time: 20 * time.Second, Output: 8 << 10})
	if _, err := r.Run(t.Context(), sp); err == nil {
		t.Fatal("output cap: no error")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	sp = child(t, "hang", l)
	sp.Caller = proc.CallerForTest("test_cancel", l)
	if _, err := r.Run(ctx, sp); err == nil {
		t.Fatal("cancel: no error")
	}
	// A holder that outlives a child that exited by itself is reported, never
	// killed, whether it stayed in the group or left it.
	for _, name := range []string{"test_held", "test_escape"} {
		pidFile := t.TempDir() + "/pid"
		killLater(t, pidFile)
		env := []string{pidFileEnv + "=" + pidFile}
		if name == "test_escape" {
			env = append(env, grandSetsEnv+"=1")
		}
		sp = child(t, "parent-exits", l, env...)
		sp.Caller = proc.CallerForTest(name, l)
		sp.WaitDelay = 200 * time.Millisecond
		if _, err := r.Run(t.Context(), sp); !errors.Is(err, proc.ErrHeldPipe) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// An output cap passed after the child had exited (a grandchild writes long
	// after the parent was reaped): counted and logged, nothing to kill.
	lateFile := t.TempDir() + "/pid"
	killLater(t, lateFile)
	lateLimits := proc.Limits{Time: 20 * time.Second, Output: 1 << 10}
	sp = child(t, "parent-exits", lateLimits, pidFileEnv+"="+lateFile, grandModeEnv+"=late-flood", splitEnv+"=4096")
	sp.Caller = proc.CallerForTest("test_late", lateLimits)
	sp.WaitDelay = 1800 * time.Millisecond
	if _, err := r.Run(t.Context(), sp); !errors.Is(err, proc.ErrOutputCap) {
		t.Fatalf("late output: %v", err)
	}

	body := scrape(t, tel)
	for _, w := range [][]string{
		{"fleet_agent_proc_kills_total{", `caller="test_time"`, `cause="time_cap"`},
		{"fleet_agent_proc_kills_total{", `caller="test_output"`, `cause="output_cap"`},
		{"fleet_agent_proc_kills_total{", `caller="test_cancel"`, `cause="cancelled"`},
		{"fleet_agent_proc_kills_total{", `caller="test_late"`, `cause="output_cap"`},
		{"fleet_agent_proc_pipe_held_total{", `caller="test_held"`},
		{"fleet_agent_proc_pipe_held_total{", `caller="test_escape"`},
		{"fleet_agent_proc_pipe_held_total{", `caller="test_late"`},
	} {
		if !hasSeries(body, w[0], w[1:]...) {
			t.Errorf("no series with count 1 for %v in:\n%s", w, body)
		}
	}
	for _, c := range []string{"test_held", "test_escape"} {
		if hasSeries(body, "fleet_agent_proc_kills_total{", `caller="`+c+`"`) {
			t.Errorf("a holder nothing killed was counted as a kill: %s", c)
		}
	}

	type kill struct {
		cause  string
		killed bool
	}
	kills := map[string]kill{}
	held := map[string]bool{}
	for _, m := range logs.lines() {
		caller, _ := m["caller"].(string)
		switch m["msg"] {
		case "proc.pipe_held":
			held[caller] = m["level"] == "warn"
		case "proc.killed":
			if m["level"] != "warn" {
				t.Errorf("proc.killed at level %v, want warn", m["level"])
			}
			cause, _ := m["cause"].(string)
			killed, _ := m["killed"].(bool)
			kills[caller] = kill{cause, killed}
			for _, k := range []string{"killed", "pid", "elapsed_ms", "output_bytes", "time_cap_ms", "output_cap_bytes"} {
				if _, ok := m[k]; !ok {
					t.Errorf("proc.killed lacks %q: %v", k, m)
				}
			}
		default:
			continue
		}
		for k := range m {
			if k == "path" || k == "args" || k == "env" || k == "output" {
				t.Errorf("%v carries %q; it may hold IDs and counts only", m["msg"], k)
			}
		}
	}
	wantKills := map[string]kill{
		"test_time":   {"time_cap", true},
		"test_output": {"output_cap", true},
		"test_cancel": {"cancelled", true},
		"test_late":   {"output_cap", false},
	}
	if len(kills) != len(wantKills) {
		t.Fatalf("logged %v, want %v", kills, wantKills)
	}
	for k, v := range wantKills {
		if kills[k] != v {
			t.Errorf("caller %s logged %+v, want %+v", k, kills[k], v)
		}
	}
	for _, c := range []string{"test_held", "test_escape", "test_late"} {
		if !held[c] {
			t.Errorf("no proc.pipe_held warn for %s", c)
		}
	}
}

// A child that writes past the cap and exits at once may be gone before the
// kill (killed=false) or still running (killed=true); either way the cap
// passed, so every run is logged and counted once.
func TestOutputCapOfAFastChildIsAlwaysSignalled(t *testing.T) {
	t.Parallel()
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "boot-2", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	r, err := proc.NewRunner(tel)
	if err != nil {
		t.Fatal(err)
	}
	const runs = 20
	l := proc.Limits{Time: 20 * time.Second, Output: 1 << 10}
	for range runs {
		sp := child(t, "split", l, splitEnv+"=2048") // 4 KiB in all, then exit 0
		sp.Caller = proc.CallerForTest("test_fast", l)
		if _, err := r.Run(deadline(t), sp); !errors.Is(err, proc.ErrOutputCap) {
			t.Fatalf("Run = %v, want ErrOutputCap", err)
		}
	}
	lines := 0
	for _, m := range logs.lines() {
		if m["msg"] == "proc.killed" && m["cause"] == "output_cap" {
			lines++
		}
	}
	if lines != runs {
		t.Errorf("%d proc.killed lines for %d runs", lines, runs)
	}
	if body := scrape(t, tel); !seriesIs(body, "fleet_agent_proc_kills_total{", strconv.Itoa(runs), `caller="test_fast"`, `cause="output_cap"`) {
		t.Errorf("the counter does not hold %d for test_fast:\n%s", runs, body)
	}
}

// The child exits by itself just under its time cap and a descendant holds the
// pipes; the time cap passes during the wait for them. The child had already
// finished, so the cap did not end anything: the result is ErrHeldPipe (exit
// 0), the holder is logged and counted, and no time cap is.
func TestTimeCapPassingDuringTheHeldWaitIsNotACap(t *testing.T) {
	t.Parallel()
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "boot-3", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	r, err := proc.NewRunner(tel)
	if err != nil {
		t.Fatal(err)
	}
	pidFile := t.TempDir() + "/pid"
	killLater(t, pidFile)
	l := proc.Limits{Time: 2 * time.Second, Output: 1 << 10} // the child exits well before, the wait (2 s) runs past it
	sp := child(t, "parent-exits", l, pidFileEnv+"="+pidFile)
	sp.Caller = proc.CallerForTest("test_capwait", l)
	res, err := r.Run(deadline(t), sp)
	if !errors.Is(err, proc.ErrHeldPipe) || errors.Is(err, proc.ErrTimeCap) || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v; want ErrHeldPipe and exit code 0", res, err)
	}
	body := scrape(t, tel)
	if !hasSeries(body, "fleet_agent_proc_pipe_held_total{", `caller="test_capwait"`) {
		t.Errorf("the holder was not counted:\n%s", body)
	}
	if strings.Contains(body, "fleet_agent_proc_kills_total{") {
		t.Errorf("a cap that ended nothing was counted:\n%s", body)
	}
	for _, m := range logs.lines() {
		if m["msg"] == "proc.killed" {
			t.Errorf("a cap that ended nothing was logged: %v", m)
		}
	}
}
