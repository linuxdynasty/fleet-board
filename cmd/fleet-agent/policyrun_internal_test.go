package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/audit"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/policy"
)

// auditRows reads audit.log in the state folder.
func auditRows(t *testing.T, dir string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, audit.File)) //nolint:gosec // G304: a test folder
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	var rows []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("audit.log line %q: %v", sc.Text(), err)
		}
		rows = append(rows, m)
	}
	return rows
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// run makes the config folder when it is missing, never writes policy.yaml,
// loads an invalid file as safe mode with an audit row, and sees an edit made
// while it runs.
func TestRunLoadsAndWatchesThePolicy(t *testing.T) {
	t.Parallel()
	s := newStartup(t)
	s.cfg = filepath.Join(t.TempDir(), "made", "fleet-agent") // does not exist yet
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	waitUntil(t, "policy.loaded", func() bool { return len(conntest.Lines(t, s.logs.String(), "policy.loaded")) == 1 })
	fi, err := os.Stat(s.cfg)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("config folder = %v, %v; want it made, mode 0700", fi, err)
	}
	if _, err := os.Stat(filepath.Join(s.cfg, policy.File)); err == nil {
		t.Error("run wrote policy.yaml; only enroll does")
	}
	if got := conntest.Lines(t, s.logs.String(), "policy.loaded")[0]; got["machine_id"] != s.machine || got["safe_mode"] != false {
		t.Errorf("policy.loaded = %v, want the machine id and no safe mode", got)
	}
	if rows := auditRows(t, s.dir); len(rows) != 0 {
		t.Errorf("audit rows after a first run = %v, want none", rows)
	}

	// An edit while running: seen without a restart, and loaded nowhere.
	bad := "schema_version: \"1\"\nmax_active: 99\n"
	if err := os.WriteFile(filepath.Join(s.cfg, policy.File), []byte(bad), 0o644); err != nil { //nolint:gosec // G306: the policy file is 0644 by design
		t.Fatal(err)
	}
	waitUntil(t, "policy.changed", func() bool { return len(conntest.Lines(t, s.logs.String(), "policy.changed")) == 1 })
	waitUntil(t, "the audit row", func() bool { return len(auditRows(t, s.dir)) == 1 })
	row := auditRows(t, s.dir)[0]
	if row["action"] != "policy.changed" {
		t.Errorf("audit row = %v, want policy.changed", row)
	}
	if lines := conntest.Lines(t, s.logs.String(), "policy.safe_mode"); len(lines) != 0 {
		t.Errorf("an edit while running put the agent in safe mode: %v (it takes effect at restart)", lines)
	}
}

func TestRunWithAnInvalidPolicyStartsInSafeModeAndAuditsIt(t *testing.T) {
	t.Parallel()
	s := newStartup(t)
	if err := os.MkdirAll(s.cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.cfg, policy.File), []byte("schema_version: \"1\"\nnot_a_key: 1\n"), 0o644); err != nil { //nolint:gosec // G306: the policy file is 0644 by design
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	waitUntil(t, "the safe-mode audit row", func() bool { return len(auditRows(t, s.dir)) == 1 })
	row := auditRows(t, s.dir)[0]
	if row["action"] != "policy.safe_mode" || row["reason"] != "unknown_key" {
		t.Errorf("audit row = %v, want policy.safe_mode / unknown_key", row)
	}
	lines := conntest.Lines(t, s.logs.String(), "policy.safe_mode")
	if len(lines) != 1 || lines[0]["error"] != "line 2: unknown key" || lines[0]["machine_id"] != s.machine {
		t.Errorf("policy.safe_mode = %v", lines)
	}
}

// close stops the watch first, then lets the loader finish (its last audit
// rows), and only then closes audit.log.
func TestPolicyRunClosesInOrder(t *testing.T) {
	t.Parallel()
	var order []string
	rec := func(name string) func() { return func() { order = append(order, name) } }
	p := &policyRun{stopWatch: rec("watch"), closeManager: rec("manager"), closeAudit: rec("audit")}
	p.close()
	if got := strings.Join(order, ","); got != "watch,manager,audit" {
		t.Errorf("close order = %s, want watch,manager,audit", got)
	}
}

// The real steps, end to end: a change found at shutdown still gets its audit
// row, because the loader finishes before audit.log closes.
func TestAChangeFoundAtShutdownIsAuditedBeforeTheLogCloses(t *testing.T) {
	t.Parallel()
	s := newStartup(t)
	if err := os.MkdirAll(s.cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	pol, err := startPolicy(context.Background(), s.cfg, s.dir, s.machine, s.tel, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Written just before shutdown, whatever the watch has or has not seen.
	// Replaced by a rename, so the watch cannot catch it half written.
	tmp := filepath.Join(s.cfg, ".policy.tmp")
	if err := os.WriteFile(tmp, []byte("schema_version: \"1\"\n"), 0o644); err != nil { //nolint:gosec // G306: the policy file is 0644 by design
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(s.cfg, policy.File)); err != nil {
		t.Fatal(err)
	}
	pol.close()
	rows := auditRows(t, s.dir)
	if len(rows) != 1 || rows[0]["action"] != "policy.changed" {
		t.Errorf("audit rows = %v, want one policy.changed", rows)
	}
}

// The machine facts and the policy each fill their own part of one Snapshot,
// in order; an error stops the chain.
func TestChainSnapshotRunsEveryHookInOrderAndStopsAtAnError(t *testing.T) {
	t.Parallel()
	var order []string
	facts := func(_ context.Context, s *agentv1.Snapshot) error {
		order = append(order, "facts")
		s.Facts = &agentv1.MachineFacts{OsVersion: "x"}
		return nil
	}
	pol := func(_ context.Context, s *agentv1.Snapshot) error {
		order = append(order, "policy")
		s.Policy = &agentv1.PolicyInfo{Hash: "h"}
		return nil
	}
	s := &agentv1.Snapshot{}
	if err := chainSnapshot(facts, pol)(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "facts,policy" || s.GetFacts().GetOsVersion() != "x" || s.GetPolicy().GetHash() != "h" {
		t.Errorf("order %v, snapshot %v; want both parts filled, facts first", order, s)
	}
	boom := errors.New("boom")
	order = nil
	fail := func(context.Context, *agentv1.Snapshot) error { return boom }
	if err := chainSnapshot(fail, pol)(context.Background(), &agentv1.Snapshot{}); !errors.Is(err, boom) || len(order) != 0 {
		t.Errorf("err = %v, hooks run after it = %v; want boom and none", err, order)
	}
}
