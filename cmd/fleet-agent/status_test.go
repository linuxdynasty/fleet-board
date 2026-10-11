package main_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// A running agent writes status.json when it starts, and once more when it
// stops on a signal, so the file shows the stop and not the last state it
// had a minute ago.
func TestARunWritesStatusJSONAtStartAndAtStop(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir, _ := enrolledDir(t, h)
	r := startAgent(t, "--state-dir", dir, "run")
	poll(t, 20*time.Second, "status.json to appear", func() bool {
		_, err := statusfile.Read(dir)
		return err == nil
	})
	fi, err := os.Stat(filepath.Join(dir, statusfile.StatusFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("status.json = %v, %v; want mode 0600", fi, err)
	}
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.done:
	case <-time.After(20 * time.Second):
		t.Fatalf("run did not stop: %s", r.stderr.String())
	}
	if r.code != 0 {
		t.Fatalf("run = %d: %s", r.code, r.stderr.String())
	}
	got, err := statusfile.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "stopped" {
		t.Errorf("final state = %q, want stopped", got.State)
	}
	if time.Since(got.WrittenTime) > time.Minute {
		t.Errorf("written_time %v is not fresh", got.WrittenTime)
	}
}

// The loop's change signal reaches the file: a run that connects shows
// state "connected" in status.json well inside the once-a-minute floor.
// Before the signal was wired the file said so only at the next minute.
func TestARunShowsItsConnectionInStatusJSONWithinSeconds(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir, _ := enrolledDir(t, h)
	startAgent(t, "--state-dir", dir, "run")
	var last string
	poll(t, 20*time.Second, "status.json to say connected", func() bool {
		s, err := statusfile.Read(dir)
		if err != nil {
			return false
		}
		last = s.State
		return s.State == "connected"
	})
	if last != "connected" {
		t.Errorf("state = %q", last)
	}
}

// A loop that ends on its own leaves the end it stopped on in status.json
// (written before the agent exits), here an expired certificate.
func TestARunThatEndsLeavesTheEndInStatusJSON(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{Now: func() time.Time {
		return time.Now().Add(-ca.AgentLifetime + 5*time.Minute + 10*time.Second)
	}})
	dir, _ := enrolledDir(t, h)
	id, err := conn.LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(id.CertNotAfter) + 100*time.Millisecond)
	r, _ := countedRun(t, dir)
	if r.code != 1 {
		t.Fatalf("run = %d: %s", r.code, r.stderr.String())
	}
	got, err := statusfile.Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "ended" || got.Ended != "cert_expired" {
		t.Errorf("status.json = state %q ended %q, want ended and cert_expired", got.State, got.Ended)
	}
}

// A failed enroll leaves its code in enroll.json (never the token), and
// the next good enroll takes it away. The state folder must exist: a failure
// before enroll makes it leaves no file and no stray folder.
func TestEnrollLeavesItsFailureCodeInEnrollJSON(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir := filepath.Join(t.TempDir(), "state")
	bad := "fbe1_short" + strings.Repeat("x", 12)
	if r := agent(t, "--state-dir", dir, "enroll", "--hub", h.Addr, "--token", bad); r.code != 1 {
		t.Fatalf("enroll = %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("a failed enroll made a state folder that enroll had not made")
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if r := agent(t, "--state-dir", dir, "enroll", "--hub", h.Addr, "--token", bad); r.code != 1 {
		t.Fatalf("enroll = %d: %s", r.code, r.stderr)
	}
	got, err := statusfile.ReadEnroll(dir)
	if err != nil || got.Code != "token_malformed" {
		t.Fatalf("enroll.json = %+v, %v; want token_malformed", got, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, statusfile.EnrollFile)) //nolint:gosec // G304: a file in the test's temp folder
	if strings.Contains(string(raw), "short") || strings.Contains(string(raw), h.Addr) {
		t.Errorf("enroll.json holds the token or the hub address:\n%s", raw)
	}
	if r := agent(t, "--state-dir", dir, "enroll", "--hub", h.Addr, "--token", h.Token(t, time.Now().Add(time.Hour))); r.code != 0 {
		t.Fatalf("enroll = %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, statusfile.EnrollFile)); err == nil {
		t.Error("enroll.json is still there after a good enroll")
	}
}

// A refused enroll on a machine that is already enrolled is not a failure
// of that machine: it writes no enroll.json.
func TestAReEnrollRefusedOnAnEnrolledMachineWritesNoEnrollJSON(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir, _ := enrolledDir(t, h)
	r := agent(t, "--state-dir", dir, "enroll", "--hub", h.Addr, "--token", h.Token(t, time.Now().Add(time.Hour)))
	if r.code != 1 || !strings.Contains(r.stderr, "already enrolled") {
		t.Fatalf("enroll = %d %q, want 1 and already enrolled", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, statusfile.EnrollFile)); err == nil {
		t.Error("a refused re-enroll left enroll.json on a healthy machine")
	}
}

// A working agent has no failed enroll: run removes a stale enroll.json
// once it has an identity.
func TestARunRemovesAStaleEnrollJSON(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	dir, _ := enrolledDir(t, h)
	stale := []byte(`{"version":1,"written_time":"2026-10-01T00:00:00Z","code":"hub_failed"}`)
	if err := os.WriteFile(filepath.Join(dir, statusfile.EnrollFile), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	startAgent(t, "--state-dir", dir, "run")
	poll(t, 20*time.Second, "status.json to appear", func() bool {
		_, err := statusfile.Read(dir)
		return err == nil
	})
	if _, err := os.Stat(filepath.Join(dir, statusfile.EnrollFile)); err == nil {
		t.Error("run left a stale enroll.json")
	}
}
