package proc_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

func hasSeries(body string, want ...string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, "fleet_agent_proc_kills_total{") {
			continue
		}
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(line, w)
		}
		if ok && strings.HasSuffix(line, " 1") {
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
	heldFile := t.TempDir() + "/pid"
	killLater(t, heldFile)
	sp = child(t, "parent-exits", l, pidFileEnv+"="+heldFile)
	sp.Caller = proc.CallerForTest("test_held", l)
	sp.WaitDelay = 200 * time.Millisecond
	if _, err := r.Run(t.Context(), sp); !errors.Is(err, proc.ErrHeldPipe) {
		t.Fatalf("held pipe: %v", err)
	}
	// A holder that left the group cannot be killed: logged, not counted.
	escFile := t.TempDir() + "/pid"
	killLater(t, escFile)
	sp = child(t, "parent-exits", l, pidFileEnv+"="+escFile, grandSetsEnv+"=1")
	sp.Caller = proc.CallerForTest("test_escape", l)
	sp.WaitDelay = 200 * time.Millisecond
	if _, err := r.Run(t.Context(), sp); !errors.Is(err, proc.ErrHeldPipe) {
		t.Fatalf("escapee: %v", err)
	}

	body := scrape(t, tel)
	for _, w := range [][]string{
		{`caller="test_time"`, `cause="time_cap"`},
		{`caller="test_output"`, `cause="output_cap"`},
		{`caller="test_cancel"`, `cause="cancelled"`},
		{`caller="test_held"`, `cause="wait_delay"`},
	} {
		if !hasSeries(body, w...) {
			t.Errorf("no series with count 1 for %v in:\n%s", w, body)
		}
	}

	if strings.Contains(body, `caller="test_escape"`) {
		t.Errorf("an unkillable holder was counted as a kill:\n%s", body)
	}
	causes := map[string]string{}
	escapeeLogged := false
	for _, m := range logs.lines() {
		if m["msg"] == "proc.pipe_held" && m["caller"] == "test_escape" && m["level"] == "warn" {
			escapeeLogged = true
		}
		if m["msg"] != "proc.killed" {
			continue
		}
		if m["caller"] == "test_held" && m["held_pipe"] != true {
			t.Errorf("the wait_delay kill lacks held_pipe=true: %v", m)
		}
		if m["level"] != "warn" {
			t.Errorf("proc.killed at level %v, want warn", m["level"])
		}
		caller, _ := m["caller"].(string)
		cause, _ := m["cause"].(string)
		causes[caller] = cause
		for _, k := range []string{"held_pipe", "pid", "elapsed_ms", "output_bytes", "time_cap_ms", "output_cap_bytes"} {
			if _, ok := m[k]; !ok {
				t.Errorf("proc.killed lacks %q: %v", k, m)
			}
		}
		for k := range m {
			if k == "path" || k == "args" || k == "env" || k == "output" {
				t.Errorf("proc.killed carries %q; it may hold IDs and counts only", k)
			}
		}
	}
	if !escapeeLogged {
		t.Error("no proc.pipe_held warn for the holder that left the group")
	}
	want := map[string]string{"test_time": "time_cap", "test_output": "output_cap", "test_cancel": "cancelled", "test_held": "wait_delay"}
	if len(causes) != len(want) {
		t.Fatalf("logged kills %v, want %v", causes, want)
	}
	for k, v := range want {
		if causes[k] != v {
			t.Errorf("caller %s logged cause %q, want %q", k, causes[k], v)
		}
	}
}
