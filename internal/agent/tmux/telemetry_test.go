package tmux_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
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

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// events returns the log lines whose msg starts with "tmux.".
func (l *lockedBuffer) events(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(l.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		if msg, _ := m["msg"].(string); strings.HasPrefix(msg, "tmux.") {
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

// series returns the value of the series whose line starts with name and
// holds every label in want, or "" when there is none.
func series(body, name string, want ...string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(line, w)
		}
		if ok {
			return line[strings.LastIndexByte(line, ' ')+1:]
		}
	}
	return ""
}

func newTelemetry(t *testing.T) (*telemetry.Telemetry, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "boot-1", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel, logs
}

// The secret word stands for pane text: it must reach neither a log line nor
// a series.
const secret = "hunter2-pane-text"

func TestFailuresAreCountedAndLoggedWithoutPaneBytes(t *testing.T) {
	t.Parallel()
	tel, logs := newTelemetry(t)
	m, err := tmux.NewMetrics(tel)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC)
	tmux.SetClockForTest(m, func() time.Time { return clock })

	long := "%output %0 " + secret + strings.Repeat("z", 64)
	in := long + "\n" + // line_too_long, outside a reply
		"%begin 1 2 1\n" + long + "\n%end 1 2 1\n" + // line_too_long inside a reply
		secret + "\n" + secret + "\n" + // stray_line twice: one line logged
		"%output %0 \\9" + secret + "\n" + // bad_escape
		"%begin 1 3 1\n" + secret + "\n" + secret + "\n%end 1 3 1\n" + // reply_too_large once
		"%exit\n"
	got := collect(t, strings.NewReader(in), tmux.Options{MaxLineBytes: 40, MaxReplyBytes: 20, Metrics: m})
	checkMessages(t, got, []string{
		`discarded line_too_long 92`,
		`reply ok [] dropped=1`,
		`discarded stray_line 17`,
		`discarded stray_line 17`,
		`discarded bad_escape 30`,
		`reply ok ["hunter2-pane-text"] dropped=1`,
		`exit ""`,
	})

	body := scrape(t, tel)
	for _, c := range []struct{ name, label, want string }{
		{"fleet_agent_tmux_lines_too_long_total", "", "2"},
		{"fleet_agent_tmux_parse_errors_total", `err_kind="stray_line"`, "2"},
		{"fleet_agent_tmux_parse_errors_total", `err_kind="bad_escape"`, "1"},
		{"fleet_agent_tmux_parse_errors_total", `err_kind="reply_too_large"`, "1"},
	} {
		if v := series(body, c.name, c.label); v != c.want {
			t.Errorf("%s{%s} = %q, want %s", c.name, c.label, v, c.want)
		}
	}
	if strings.Contains(body, secret) {
		t.Error("the scrape holds pane text")
	}

	ev := logs.events(t)
	want := []string{
		`tmux.line_too_long bytes=92 limit_bytes=40 suppressed=0`,
		`tmux.parse_error err_kind=stray_line bytes=17 suppressed=0`,
		`tmux.parse_error err_kind=bad_escape bytes=30 suppressed=0`,
		`tmux.parse_error err_kind=reply_too_large bytes=34 suppressed=0`,
	}
	var gotEv []string
	for _, e := range ev {
		s := e["msg"].(string)
		for _, k := range []string{"err_kind", "bytes", "limit_bytes", "suppressed"} {
			if v, ok := e[k]; ok {
				b, _ := json.Marshal(v)
				s += " " + k + "=" + strings.Trim(string(b), `"`)
			}
		}
		if e["level"] != "warn" || e["component"] != "agent.tmux" {
			t.Errorf("%s: level %v, component %v", e["msg"], e["level"], e["component"])
		}
		gotEv = append(gotEv, s)
	}
	checkMessages(t, gotEv, want)
	if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), "zzzz") {
		t.Errorf("a log line holds pane text:\n%s", logs.String())
	}

	// A minute later the next line of a class logs again with the count of
	// the lines it did not log.
	clock = clock.Add(time.Minute)
	collect(t, strings.NewReader("x\n"), tmux.Options{Metrics: m})
	last := logs.events(t)
	if e := last[len(last)-1]; e["err_kind"] != "stray_line" || e["suppressed"] != float64(1) {
		t.Errorf("after a minute: %v", e)
	}
}

func TestNewMetricsNeedsTelemetry(t *testing.T) {
	t.Parallel()
	if _, err := tmux.NewMetrics(nil); err == nil {
		t.Fatal("NewMetrics(nil) returned no error")
	}
}
