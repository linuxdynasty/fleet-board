package policy_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const goodFile = "schema_version: \"1\"\nmax_active: 3\n"

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

type auditSink struct {
	mu   sync.Mutex
	rows []audit.Entry
}

func (a *auditSink) Append(e audit.Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rows = append(a.rows, e)
}

func (a *auditSink) all() []audit.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]audit.Entry(nil), a.rows...)
}

type rig struct {
	dir     string
	path    string
	logs    *conntest.Buffer
	tel     *telemetry.Telemetry
	audit   *auditSink
	changes chan policy.Change
	now     time.Time
}

func newRig(t *testing.T, file string) *rig {
	t.Helper()
	logs := &conntest.Buffer{}
	tel, err := telemetry.New(telemetry.Config{Service: telemetry.Agent, Version: "test", InstanceID: "boot", LogOutput: logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	dir := t.TempDir()
	r := &rig{
		dir: dir, path: filepath.Join(dir, policy.File), logs: logs, tel: tel, audit: &auditSink{},
		changes: make(chan policy.Change, 32), now: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC),
	}
	if file != "" {
		r.write(t, file)
	}
	return r
}

func (r *rig) opts() policy.Options {
	return policy.Options{
		Dir: r.dir, Telemetry: r.tel, Audit: r.audit, Now: func() time.Time { return r.now },
		OnChange: func(c policy.Change) { r.changes <- c }, Settle: 40 * time.Millisecond,
	}
}

func (r *rig) load(t *testing.T) *policy.Manager {
	t.Helper()
	m, err := policy.Load(r.opts())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func (r *rig) write(t *testing.T, s string) {
	t.Helper()
	if err := os.WriteFile(r.path, []byte(s), 0o644); err != nil { //nolint:gosec // G306: the policy file is 0644 by design (03e)
		t.Fatal(err)
	}
}

func (r *rig) scrape(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	r.tel.MetricsHandler(nil).ServeHTTP(rec, req)
	return rec.Body.String()
}

// metric is the value of the sample named name, or "" when there is none.
func (r *rig) metric(t *testing.T, name string) string {
	t.Helper()
	for line := range strings.Lines(r.scrape(t)) {
		rest, ok := strings.CutPrefix(line, name)
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '{') {
			continue
		}
		if i := strings.LastIndexByte(rest, ' '); i >= 0 {
			return strings.TrimSpace(rest[i+1:])
		}
	}
	return ""
}

func (r *rig) events(t *testing.T, prefix string) []map[string]any {
	t.Helper()
	return conntest.Lines(t, r.logs.String(), prefix)
}

func (r *rig) nextChange(t *testing.T) policy.Change {
	t.Helper()
	select {
	case c := <-r.changes:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no policy change within 10s")
		return policy.Change{}
	}
}

func (r *rig) noChange(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case c := <-r.changes:
		t.Fatalf("unexpected change %+v", c)
	case <-time.After(within):
	}
}

// A fresh agent has no policy.yaml: a normal first run (03e, ADR 0024).
func TestAMissingFileIsANormalFirstRun(t *testing.T) {
	t.Parallel()
	r := newRig(t, "")
	m := r.load(t)
	info := m.Info()
	if info.GetSchemaVersion() != "" || info.GetHash() != "" || info.GetSafeMode() || info.GetError() != "" || info.GetChangedSinceLoad() {
		t.Errorf("Info = %v, want an empty hash and schema_version, no safe mode", info)
	}
	if !info.GetLoadedTime().AsTime().Equal(r.now) {
		t.Errorf("loaded_time = %v, want %v", info.GetLoadedTime().AsTime(), r.now)
	}
	p := m.Policy()
	if len(p.AllowedRepoRoots) != 0 || p.AllowedHarnesses != nil || m.SafeMode() {
		t.Errorf("Policy = %+v, want no roots and no opinion on harnesses", p)
	}
	ev := r.events(t, "policy.loaded")
	if len(ev) != 1 || ev[0]["safe_mode"] != false || ev[0]["hash"] != "" || ev[0]["schema_version"] != "" {
		t.Errorf("policy.loaded = %v", ev)
	}
	if len(r.audit.all()) != 0 || len(r.events(t, "policy.safe_mode")) != 0 {
		t.Error("a missing file wrote an audit row or a safe-mode line")
	}
}

func TestAValidFileIsLoadedAndHashed(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	info := m.Info()
	if info.GetHash() != sum(goodFile) || info.GetSchemaVersion() != "1" || info.GetSafeMode() || info.GetChangedSinceLoad() {
		t.Errorf("Info = %v, want hash %s, schema 1", info, sum(goodFile))
	}
	if got := m.Policy().MaxActive; got == nil || *got != 3 {
		t.Errorf("max_active = %v, want 3", got)
	}
	ev := r.events(t, "policy.loaded")
	if len(ev) != 1 || ev[0]["hash"] != sum(goodFile) || ev[0]["schema_version"] != "1" {
		t.Errorf("policy.loaded = %v", ev)
	}
	if strings.Contains(r.logs.String(), "max_active") {
		t.Error("a log line carries file text")
	}
}

func TestAnInvalidFileIsSafeModeWithAOneLineReasonAndAnAuditRow(t *testing.T) {
	t.Parallel()
	bad := "schema_version: \"1\"\nmax_active: " + secret + "\n"
	r := newRig(t, bad)
	m := r.load(t)
	info := m.Info()
	if !info.GetSafeMode() || info.GetError() != "line 2: max_active: must be a whole number" {
		t.Errorf("Info = %v, want safe mode and the one-line reason", info)
	}
	if info.GetHash() != sum(bad) || info.GetSchemaVersion() != "1" {
		t.Errorf("Info = %v, want the hash of the file that was read", info)
	}
	if !m.SafeMode() || len(m.Policy().AllowedRepoRoots) != 0 {
		t.Error("safe mode must leave the built-in defaults, with no roots")
	}
	rows := r.audit.all()
	if len(rows) != 1 || rows[0].Action != audit.PolicySafeMode || rows[0].Reason != "bad_type" || rows[0].Detail["hash"] != sum(bad) {
		t.Errorf("audit rows = %+v, want one policy.safe_mode with the code and the hash", rows)
	}
	if ev := r.events(t, "policy.safe_mode"); len(ev) != 1 || ev[0]["error"] != info.GetError() {
		t.Errorf("policy.safe_mode = %v", ev)
	}
	if strings.Contains(r.logs.String(), secret) {
		t.Error("the file's text reached a log line")
	}
	if v := r.metric(t, "fleet_agent_policy_safe_mode"); v != "1" {
		t.Errorf("fleet_agent_policy_safe_mode = %q, want 1", v)
	}
}

func TestAnUnreadableFileIsSafeModeWithNoHash(t *testing.T) {
	t.Parallel()
	r := newRig(t, "")
	if err := os.Mkdir(r.path, 0o755); err != nil { //nolint:gosec // G301: a test folder
		t.Fatal(err)
	}
	info := r.load(t).Info()
	if !info.GetSafeMode() || info.GetHash() != "" || info.GetSchemaVersion() != "" {
		t.Errorf("Info = %v, want safe mode with no hash (it could not be read)", info)
	}
	if rows := r.audit.all(); len(rows) != 1 || rows[0].Reason != "unreadable" {
		t.Errorf("audit rows = %+v", rows)
	}
}

func TestAFileOverTheSizeCapIsSafeMode(t *testing.T) {
	t.Parallel()
	r := newRig(t, "schema_version: \"1\"\n# "+strings.Repeat("x", 70<<10)+"\n")
	info := r.load(t).Info()
	if !info.GetSafeMode() || info.GetError() != "the file is over 64 KiB" {
		t.Errorf("Info = %v, want safe mode: too large", info)
	}
}

func TestExecRawIsIgnoredAndLoggedOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t, "schema_version: \"1\"\nexec:\n  raw: true\n")
	m := r.load(t)
	if m.SafeMode() {
		t.Fatal("exec.raw put the agent in safe mode; it is known and ignored")
	}
	if ev := r.events(t, "policy.key_ignored"); len(ev) != 1 || ev[0]["key"] != "exec.raw" {
		t.Errorf("policy.key_ignored = %v", ev)
	}
}

func TestAHashChangeIsReportedAndChangesNothingLive(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	next := "schema_version: \"1\"\nmax_active: 9\n"
	r.write(t, next)
	r.now = r.now.Add(time.Hour)
	m.Recheck(t.Context())
	c := r.nextChange(t)
	if c.PreviousHash != sum(goodFile) || c.Hash != sum(next) || !c.Time.Equal(r.now) {
		t.Errorf("Change = %+v", c)
	}
	if c.Info.GetHash() != sum(next) || !c.Info.GetChangedSinceLoad() {
		t.Errorf("Change.Info = %v, want the new hash and changed_since_load", c.Info)
	}
	info := m.Info()
	if !info.GetChangedSinceLoad() || info.GetHash() != sum(goodFile) {
		t.Errorf("Info = %v, want the loaded hash and changed_since_load", info)
	}
	if got := m.Policy().MaxActive; got == nil || *got != 3 {
		t.Errorf("max_active = %v after the file changed, want the loaded 3 (takes effect at restart)", got)
	}
	rows := r.audit.all()
	if len(rows) != 1 || rows[0].Action != audit.PolicyChanged || rows[0].Detail["old_hash"] != sum(goodFile) || rows[0].Detail["new_hash"] != sum(next) {
		t.Errorf("audit rows = %+v", rows)
	}
	if ev := r.events(t, "policy.changed"); len(ev) != 1 || ev[0]["old_hash"] != sum(goodFile) || ev[0]["new_hash"] != sum(next) {
		t.Errorf("policy.changed = %v", ev)
	}
	if v := r.metric(t, "fleet_agent_policy_changed_total"); v != "1" {
		t.Errorf("fleet_agent_policy_changed_total = %q, want 1", v)
	}
	m.Recheck(t.Context())
	r.noChange(t, 50*time.Millisecond)
	// Back to what was loaded: a change again, and changed_since_load clears.
	r.write(t, goodFile)
	m.Recheck(t.Context())
	if c := r.nextChange(t); c.Hash != sum(goodFile) || c.Info.GetChangedSinceLoad() {
		t.Errorf("Change back = %+v", c)
	}
	if m.Info().GetChangedSinceLoad() {
		t.Error("changed_since_load stays set after the file went back to the loaded content")
	}
}

func TestADeletedFileCountsAsAChangeAndACreatedOneToo(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	if err := os.Remove(r.path); err != nil {
		t.Fatal(err)
	}
	m.Recheck(t.Context())
	c := r.nextChange(t)
	if c.PreviousHash != sum(goodFile) || c.Hash != "" || !m.Info().GetChangedSinceLoad() {
		t.Errorf("after delete: Change = %+v, Info = %v", c, m.Info())
	}
	r.write(t, goodFile)
	m.Recheck(t.Context())
	if c := r.nextChange(t); c.PreviousHash != "" || c.Hash != sum(goodFile) {
		t.Errorf("after recreate: Change = %+v", c)
	}

	r2 := newRig(t, "")
	m2 := r2.load(t)
	r2.write(t, goodFile)
	m2.Recheck(t.Context())
	if c := r2.nextChange(t); c.PreviousHash != "" || c.Hash != sum(goodFile) || !m2.Info().GetChangedSinceLoad() {
		t.Errorf("a file created after a first run: Change = %+v", c)
	}
}

func TestSnapshotRehashesAtBuildTime(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	r.write(t, goodFile+"max_total: 5\n")
	snap := &agentv1.Snapshot{}
	if err := m.Snapshot(t.Context(), snap); err != nil {
		t.Fatal(err)
	}
	if !snap.GetPolicy().GetChangedSinceLoad() || snap.GetPolicy().GetHash() != sum(goodFile) {
		t.Errorf("Snapshot.policy = %v, want the loaded hash and changed_since_load (no watcher ran)", snap.GetPolicy())
	}
	r.nextChange(t)
}

func TestAnUnreadableFileAtRecheckChangesNothingAndIsLogged(t *testing.T) {
	t.Parallel()
	r := newRig(t, goodFile)
	m := r.load(t)
	if err := os.Remove(r.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(r.path, 0o755); err != nil { //nolint:gosec // G301: a test folder
		t.Fatal(err)
	}
	m.Recheck(t.Context())
	r.noChange(t, 50*time.Millisecond)
	if ev := r.events(t, "policy.check_failed"); len(ev) != 1 || ev[0]["err_kind"] != "io" {
		t.Errorf("policy.check_failed = %v", ev)
	}
}
