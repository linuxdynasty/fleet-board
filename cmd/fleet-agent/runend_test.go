package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
)

// countedRun points dir's cached hub address at a counting listener, runs
// fleet-agent run, and waits up to 10 s for it to exit (a run that keeps
// going fails the test instead of hanging it).
func countedRun(t *testing.T, dir string) (*running, int64) {
	t.Helper()
	addr, dialed := countingHub(t)
	if err := os.WriteFile(filepath.Join(dir, "hub-address"), []byte(addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := startAgent(t, "--state-dir", dir, "run")
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("run is still running: %s", r.stderr.String())
	}
	return r, dialed.Load()
}

// A damaged ended.json is refused, not guessed at: exit 1, one
// run.failed{step="ended", err_kind="parse"} with the machine id, and no
// connection to the hub.
func TestARunWithADamagedEndSendsNothing(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir, machine := enrolledDir(t, h)
	if err := os.WriteFile(filepath.Join(dir, "ended.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, dialed := countedRun(t, dir)
	out := r.stderr.String()
	failed := conntest.Lines(t, out, "run.failed")
	if r.code != 1 || len(failed) != 1 || failed[0]["step"] != "ended" || failed[0]["err_kind"] != "parse" ||
		failed[0]["machine_id"] != machine {
		t.Errorf("run = %d %q; want 1 and one run.failed{step=ended, err_kind=parse, machine_id=%s}", r.code, out, machine)
	}
	if dialed != 0 {
		t.Errorf("run with a damaged ended.json made %d connections, want none", dialed)
	}
	if lines := conntest.Lines(t, out, "connect.attempt"); len(lines) != 0 {
		t.Errorf("run with a damaged ended.json tried to connect to %v", lines)
	}
}

// An ended.json open to other users is refused the same way, as a
// constraint (03 W1): exit 1, run.failed{step="ended", err_kind="constraint"}
// and no connection.
func TestARunWithALooseEndSendsNothing(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir, machine := enrolledDir(t, h)
	writeEnded(t, dir, "revoked", machine)
	if err := os.Chmod(filepath.Join(dir, "ended.json"), 0o644); err != nil { //nolint:gosec // G302: the test makes the file loose on purpose
		t.Fatal(err)
	}
	r, dialed := countedRun(t, dir)
	out := r.stderr.String()
	failed := conntest.Lines(t, out, "run.failed")
	if r.code != 1 || len(failed) != 1 || failed[0]["step"] != "ended" || failed[0]["err_kind"] != "constraint" ||
		failed[0]["machine_id"] != machine {
		t.Errorf("run = %d %q; want 1 and one run.failed{step=ended, err_kind=constraint, machine_id=%s}", r.code, out, machine)
	}
	if dialed != 0 {
		t.Errorf("run with a loose ended.json made %d connections, want none", dialed)
	}
	if lines := conntest.Lines(t, out, "connect.attempt"); len(lines) != 0 {
		t.Errorf("run with a loose ended.json tried to connect to %v", lines)
	}
}

// The loop ending on its own exits 1: here the certificate has expired
// (the test rewrites its own identity fixture), so the loop ends before it
// dials, logs connect.ended{state=cert_expired}, and run exits 1.
func TestARunWhoseLoopEndsExitsOne(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir, _ := enrolledDir(t, h)
	path := filepath.Join(dir, "identity.json")
	data, err := os.ReadFile(path) //nolint:gosec // G304: the test's own state folder
	if err != nil {
		t.Fatal(err)
	}
	var id map[string]any
	if err := json.Unmarshal(data, &id); err != nil {
		t.Fatal(err)
	}
	id["cert_not_after"] = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if data, err = json.Marshal(id); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	r, dialed := countedRun(t, dir)
	out := r.stderr.String()
	ended := conntest.Lines(t, out, "connect.ended")
	if r.code != 1 || len(ended) != 1 || ended[0]["state"] != "cert_expired" {
		t.Errorf("run = %d %q; want 1 and connect.ended{state=cert_expired}", r.code, out)
	}
	if dialed != 0 {
		t.Errorf("run with an expired certificate made %d connections, want none", dialed)
	}
	if lines := conntest.Lines(t, out, "connect.attempt"); len(lines) != 0 {
		t.Errorf("run with an expired certificate tried to connect to %v", lines)
	}
}
