package events_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/events"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// logBuf is a log sink the logger and the test share.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// events returns the log lines whose msg is name.
func (l *logBuf) events(name string) []map[string]any {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(l.b.Bytes()))
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && m["msg"] == name {
			out = append(out, m)
		}
	}
	return out
}

type rig struct {
	dir  string
	db   *ring.DB
	tel  *telemetry.Telemetry
	logs *logBuf
	e    *events.Emitter
}

func stateDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fleet-agent")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// newRig opens agent.db in dir and an emitter on it; opts.DB and
// opts.Telemetry are filled in.
func newRig(t *testing.T, dir string, opts events.Options) *rig {
	t.Helper()
	logs := &logBuf{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "v0", InstanceID: "b", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	db, err := ring.Open(t.Context(), dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	opts.DB, opts.Telemetry = db, tel
	e, err := events.New(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{dir: dir, db: db, tel: tel, logs: logs, e: e}
	t.Cleanup(r.close)
	return r
}

func (r *rig) close() {
	r.e.Close()
	_ = r.db.Close()
	_ = r.tel.Shutdown(context.Background())
}

func (r *rig) marks(t *testing.T) ring.Marks {
	t.Helper()
	st, err := r.db.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return st.Marks
}

func (r *rig) metric(t *testing.T, name string, labels ...string) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	r.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	for line := range strings.SplitSeq(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		ok := true
		for _, w := range labels {
			ok = ok && strings.Contains(line, w)
		}
		if ok {
			return line[strings.LastIndex(line, " ")+1:]
		}
	}
	return ""
}

func policyEv(hash string) *agentv1.Event {
	return &agentv1.Event{Body: &agentv1.Event_PolicyChanged{PolicyChanged: &agentv1.PolicyChanged{PreviousHash: hash}}}
}

func sampleEv() *agentv1.Event {
	return &agentv1.Event{Body: &agentv1.Event_ResourceSample{ResourceSample: &agentv1.ResourceSample{}}}
}

func inputEv(paneID string) *agentv1.Event {
	return &agentv1.Event{Body: &agentv1.Event_PaneInput{PaneInput: &agentv1.PaneInput{Pane: pane2(paneID)}}}
}

func emit(t *testing.T, e *events.Emitter, ev *agentv1.Event) uint64 {
	t.Helper()
	seq, err := e.Emit(t.Context(), ev)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return seq
}

// drain reads the cursor until it has nothing more.
func drain(t *testing.T, c *events.Cursor) []*agentv1.Event {
	t.Helper()
	var out []*agentv1.Event
	for {
		got, more, err := c.Next(t.Context(), 64)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, got...)
		if !more && len(got) == 0 {
			return out
		}
	}
}

func seqs(es []*agentv1.Event) []uint64 {
	out := make([]uint64, 0, len(es))
	for _, e := range es {
		out = append(out, e.GetSeq())
	}
	return out
}
