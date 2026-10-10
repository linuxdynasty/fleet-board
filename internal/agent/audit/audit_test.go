package audit_test

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
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

type buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type rig struct {
	dir  string
	log  *audit.Log
	logs *buffer
	tel  *telemetry.Telemetry
	now  *time.Time
}

func newRig(t *testing.T, set func(*audit.Options)) *rig {
	t.Helper()
	logs := &buffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "boot", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	r := &rig{dir: t.TempDir(), logs: logs, tel: tel, now: &now}
	opts := audit.Options{Telemetry: tel, Now: func() time.Time { return *r.now }}
	if set != nil {
		set(&opts)
	}
	r.log, err = audit.New(r.dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.log.Close() })
	return r
}

func (r *rig) lines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join(r.dir, name)) //nolint:gosec // G304: a test folder
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

func (r *rig) scrape(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	r.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

func TestAppendWritesOneClosedJSONLinePerEntry(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	r.log.Append(audit.Entry{
		Action: audit.PolicyChanged,
		Detail: map[string]string{"old_hash": "aa11", "new_hash": "bb22"},
	})
	r.log.Append(audit.Entry{
		Action: audit.CommandRefused, CommandID: "01J8", Kind: "KillWindow", Reason: "POLICY_SAFE_MODE",
		Status: "REJECTED_BY_POLICY", RequestedBy: &audit.Principal{Kind: "USER", ID: "u1"},
	})
	got := r.lines(t, audit.File)
	if len(got) != 2 {
		t.Fatalf("lines = %d, want 2: %q", len(got), got)
	}
	want0 := `{"time":"2026-10-10T12:00:00Z","action":"policy.changed","detail":{"new_hash":"bb22","old_hash":"aa11"}}`
	if got[0] != want0 {
		t.Errorf("line 0 = %s\nwant     %s", got[0], want0)
	}
	want1 := `{"time":"2026-10-10T12:00:00Z","action":"command.refused","command_id":"01J8","kind":"KillWindow",` +
		`"reason":"POLICY_SAFE_MODE","status":"REJECTED_BY_POLICY","requested_by":{"kind":"USER","id":"u1"}}`
	if got[1] != want1 {
		t.Errorf("line 1 = %s\nwant     %s", got[1], want1)
	}
	fi, err := os.Stat(filepath.Join(r.dir, audit.File))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("audit.log mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestAppendRefusesFreeTextAndUnknownActions(t *testing.T) {
	t.Parallel()
	bad := []audit.Entry{
		{Action: "command.invented"},
		{Action: audit.CommandRefused, Reason: "the agent said: /Users/me/secret.txt"},
		{Action: audit.CommandRefused, Kind: "line\nbreak"},
		{Action: audit.PolicyChanged, Detail: map[string]string{"old_hash": "has space"}},
		{Action: audit.PolicyChanged, Detail: map[string]string{"free text": "x"}},
		{Action: audit.PolicyChanged, Detail: map[string]string{"a": "1", "b": "1", "c": "1", "d": "1", "e": "1"}},
		{Action: audit.CommandRefused, Reason: strings.Repeat("a", 129)},
	}
	for i, e := range bad {
		r := newRig(t, nil)
		r.log.Append(e)
		if _, err := os.Stat(filepath.Join(r.dir, audit.File)); err == nil {
			t.Errorf("case %d: an entry with free text or an unknown action was written", i)
		}
		if m := r.scrape(t); !strings.Contains(m, `fleet_agent_audit_write_failures_total{err_kind="constraint"`) {
			t.Errorf("case %d: no constraint failure counted:\n%s", i, m)
		}
	}
}

func TestAFailedWriteNeverBlocksAndIsCountedAndLoggedOncePerMinute(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	// A folder where the file belongs: opening it for append fails.
	if err := os.Mkdir(filepath.Join(r.dir, audit.File), 0o700); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		r.log.Append(audit.Entry{Action: audit.PolicySafeMode, Reason: "bad_value"})
	}
	*r.now = r.now.Add(61 * time.Second)
	r.log.Append(audit.Entry{Action: audit.PolicySafeMode, Reason: "bad_value"})
	if n := strings.Count(r.logs.String(), `"msg":"audit.write_failed"`); n != 2 {
		t.Errorf("audit.write_failed lines = %d, want 2 (first, then one a minute later):\n%s", n, r.logs.String())
	}
	if !strings.Contains(r.logs.String(), `"suppressed":2`) {
		t.Errorf("second line should count the 2 suppressed ones:\n%s", r.logs.String())
	}
	m := r.scrape(t)
	if !strings.Contains(m, `fleet_agent_audit_write_failures_total{`) || !strings.Contains(m, `err_kind="io"`) {
		t.Errorf("failure counter missing:\n%s", m)
	}
	for _, line := range strings.Split(m, "\n") {
		if strings.HasPrefix(line, "fleet_agent_audit_write_failures_total{") && !strings.HasSuffix(line, " 4") {
			t.Errorf("counter line = %q, want 4 (every failure is counted)", line)
		}
	}
	if strings.Contains(r.logs.String(), r.dir) {
		t.Errorf("a log line carries the path:\n%s", r.logs.String())
	}
}

func TestTheFileIsRotatedAtTheSizeCapKeepingThreeFiles(t *testing.T) {
	t.Parallel()
	r := newRig(t, func(o *audit.Options) { o.MaxBytes = 400 })
	for i := range 40 {
		r.log.Append(audit.Entry{Action: audit.PolicyChanged, Detail: map[string]string{"n": string(rune('a' + i%26))}})
	}
	total := 0
	for _, name := range []string{audit.File, audit.File + ".1", audit.File + ".2"} {
		fi, err := os.Stat(filepath.Join(r.dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fi.Size() > 400 {
			t.Errorf("%s is %d bytes, over the 400 cap", name, fi.Size())
		}
		total += len(r.lines(t, name))
	}
	if _, err := os.Stat(filepath.Join(r.dir, audit.File+".3")); err == nil {
		t.Error("audit.log.3 exists; three files are kept")
	}
	if total >= 40 || total < 6 {
		t.Errorf("kept %d lines of 40; the oldest are dropped, the newest kept", total)
	}
	last := r.lines(t, audit.File)
	var e struct{ Detail map[string]string }
	if err := json.Unmarshal([]byte(last[len(last)-1]), &e); err != nil || e.Detail["n"] != string(rune('a'+39%26)) {
		t.Errorf("newest line = %q (%v), want the 40th entry", last[len(last)-1], err)
	}
}

func TestADeletedOrReplacedFileIsReopened(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	r.log.Append(audit.Entry{Action: audit.PolicyChanged})
	if err := os.Remove(filepath.Join(r.dir, audit.File)); err != nil {
		t.Fatal(err)
	}
	r.log.Append(audit.Entry{Action: audit.PolicySafeMode})
	got := r.lines(t, audit.File)
	if len(got) != 1 || !strings.Contains(got[0], "policy.safe_mode") {
		t.Errorf("after the file was deleted, lines = %q, want the new entry in a new file", got)
	}
}

func TestATornLastLineDoesNotGlueToTheNextEntry(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	if err := os.WriteFile(filepath.Join(r.dir, audit.File), []byte(`{"time":"2026-10-1`), 0o600); err != nil {
		t.Fatal(err)
	}
	r.log.Append(audit.Entry{Action: audit.PolicyChanged})
	got := r.lines(t, audit.File)
	if len(got) != 2 || !json.Valid([]byte(got[1])) {
		t.Errorf("lines = %q, want the torn line, then one whole entry", got)
	}
}

func TestConcurrentAppendsKeepWholeLines(t *testing.T) {
	t.Parallel()
	r := newRig(t, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				r.log.Append(audit.Entry{Action: audit.PolicyChanged, Detail: map[string]string{"k": "v"}})
			}
		})
	}
	wg.Wait()
	got := r.lines(t, audit.File)
	if len(got) != 160 {
		t.Fatalf("lines = %d, want 160", len(got))
	}
	for _, l := range got {
		if !json.Valid([]byte(l)) {
			t.Fatalf("torn line %q", l)
		}
	}
}
