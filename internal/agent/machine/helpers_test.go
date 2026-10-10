package machine_test

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

	"github.com/linuxdynasty/fleet-board/internal/agent/machine"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// lockedBuffer is a log sink the logger and the test share.
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

// events returns the log lines whose msg is name.
func (l *lockedBuffer) events(name string) []map[string]any {
	var out []map[string]any
	for _, m := range l.lines() {
		if m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

// newTel returns telemetry that logs into the returned buffer, and Metrics on it.
func newTel(t *testing.T) (*telemetry.Telemetry, *machine.Metrics, *lockedBuffer) {
	t.Helper()
	logs := &lockedBuffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "boot-1", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	m, err := machine.NewMetrics(tel)
	if err != nil {
		t.Fatal(err)
	}
	return tel, m, logs
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

// seriesValue returns the value of the series that starts with prefix and has
// every want in its labels, or "" when there is none.
func seriesValue(body, prefix string, want ...string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		ok := true
		for _, w := range want {
			ok = ok && strings.Contains(line, w)
		}
		if ok {
			return line[strings.LastIndex(line, " ")+1:]
		}
	}
	return ""
}
