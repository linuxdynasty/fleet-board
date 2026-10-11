//go:build unix

package doctor_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

// writeEnded puts an ended.json the way the loop writes it (mode 0600),
// written an hour before now by the hub at hub.example:7443.
func (r *rig) writeEnded(end, machine string) {
	r.t.Helper()
	r.write(r.state, "ended.json",
		`{"version":1,"end":"`+end+`","machine_id":"`+machine+`","peer":"hub.example:7443","time":"`+now.Add(-time.Hour).Format(time.RFC3339)+`"}`, 0o600)
}

func (r *rig) statusEnded(end string) {
	r.t.Helper()
	r.writeStatus(time.Hour, func(s *statusfile.Status) { s.State, s.Ended = "ended", end })
}

// `run` decides "will not reconnect" from ended.json (and the certificate
// for cert_expired), not from status.json, which can be a leftover.
func TestConnectionTheEndIsJudgedFromWhatRunReads(t *testing.T) {
	t.Parallel()
	t.Run("an old end in status.json with no ended.json is not a failure", func(t *testing.T) {
		t.Parallel()
		for _, end := range []string{"revoked", "unknown_machine", "fleet_mismatch"} {
			r := newRig(t)
			r.enroll(time.Hour)
			r.statusEnded(end)
			r.want("connection", doctor.Unknown, "stopped on "+end, "1h0m0s ago", "cleared", "next run will try again", "start the agent", "fleet-agent run")
		}
	})
	t.Run("ended.json for this machine fails with its end, hub and time", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ end, words string }{
			{"revoked", "new token"},
			{"unknown_machine", "new token"},
			{"fleet_mismatch", "another fleet"},
		} {
			r := newRig(t)
			r.enroll(time.Hour)
			r.writeEnded(tc.end, machineID)
			r.want("connection", doctor.Fail, tc.end, "hub.example:7443", "1h0m0s ago", tc.words)
		}
	})
	t.Run("ended.json is enough, whatever status.json says", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		r.writeEnded("revoked", machineID)
		r.want("connection", doctor.Fail, "revoked")
		r.writeStatus(time.Second, func(s *statusfile.Status) { s.State = "connected" })
		r.want("connection", doctor.Fail, "revoked")
	})
	t.Run("identity.json unreadable: ended.json still fails with its end", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeEnded("unknown_machine", machineID)
		r.want("connection", doctor.Fail, "unknown_machine", "hub.example:7443")
	})
	t.Run("ended.json for another machine is a leftover, not a failure", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		r.writeEnded("revoked", "m_someoneelse0")
		r.want("connection", doctor.Unknown, "leftover from an earlier enrollment", "next run removes it", "start the agent")
	})
	t.Run("a damaged ended.json fails: run exits 1 until it is removed", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		r.write(r.state, "ended.json", "{nope", 0o600)
		r.want("connection", doctor.Fail, "run exits 1 before any network call", "remove it to try again")
	})
	t.Run("an ended.json from another version or with a made-up end is damaged", func(t *testing.T) {
		t.Parallel()
		for _, body := range []string{
			`{"version":9,"end":"revoked","machine_id":"` + machineID + `","peer":"p","time":"2026-10-11T11:00:00Z"}`,
			`{"version":1,"end":"cert_expired","machine_id":"` + machineID + `","peer":"p","time":"2026-10-11T11:00:00Z"}`,
		} {
			r := newRig(t)
			r.enroll(time.Hour)
			r.write(r.state, "ended.json", body, 0o600)
			r.want("connection", doctor.Fail, "remove it to try again")
		}
	})
	t.Run("a hub address with control characters is cleaned", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		r.write(r.state, "ended.json", `{"version":1,"end":"revoked","machine_id":"`+machineID+`","peer":"evil\u001b[2Jhub:1","time":"2026-10-11T11:00:00Z"}`, 0o600)
		c := r.want("connection", doctor.Fail, "revoked")
		if bytes.ContainsRune([]byte(c.Reason+c.Fix), 0x1b) {
			t.Errorf("escape in %q", c.Reason)
		}
	})
	t.Run("doctor never changes or removes ended.json", func(t *testing.T) {
		t.Parallel()
		for _, machine := range []string{machineID, "m_someoneelse0"} {
			r := newRig(t)
			r.enroll(time.Hour)
			r.writeEnded("revoked", machine)
			path := filepath.Join(r.state, "ended.json")
			before, err := os.ReadFile(path) //nolint:gosec // G304: a file in the test's temp folder
			if err != nil {
				t.Fatal(err)
			}
			r.run()
			after, err := os.ReadFile(path) //nolint:gosec // G304: a file in the test's temp folder
			if err != nil || !bytes.Equal(before, after) {
				t.Errorf("ended.json for %s changed: %v %q", machine, err, after)
			}
		}
	})
}

func TestConnectionCertExpiredEndIsJudgedFromTheCertificate(t *testing.T) {
	t.Parallel()
	t.Run("a valid certificate now: re-enrolled since", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.enroll(time.Hour)
		r.statusEnded("cert_expired")
		r.want("connection", doctor.Unknown, "cert_expired", "re-enrolled since", "start the agent")
	})
	t.Run("an expired certificate now fails", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeIdentity(r.leaf(machineID, now.Add(-100*24*time.Hour), now.Add(-10*24*time.Hour)), now.Add(-10*24*time.Hour))
		r.statusEnded("cert_expired")
		r.want("connection", doctor.Fail, "cert_expired", "re-enroll")
	})
	t.Run("no readable identity: unknown, the identity check reports its own failure", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.statusEnded("cert_expired")
		r.want("connection", doctor.Unknown, "identity")
	})
}

// A FIFO named ended.json must not hold doctor up (open(2) on one blocks
// for good): it is a file that is not whole, so a failure.
func TestAFifoNamedEndedJsonDoesNotHangDoctor(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.enroll(time.Hour)
	fifo := filepath.Join(r.state, "ended.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan doctor.Result, 1)
	go func() { done <- r.get("connection") }()
	select {
	case c := <-done:
		if c.Status != doctor.Fail {
			t.Errorf("connection: status = %s (%q), want fail", c.Status, c.Reason)
		}
	case <-time.After(5 * time.Second):
		unblock(fifo)
		t.Fatal("doctor did not finish: a FIFO named ended.json blocked it")
	}
}

// CheckEnded removes another machine's ended.json; doctor reads it with
// ReadEnded and must never call the function that removes.
func TestDoctorNeverCallsTheFunctionThatRemovesEndedJson(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f) //nolint:gosec // G304: a file name from Glob in this package's folder
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(src, []byte("conn.CheckEnded(")) {
			t.Errorf("%s calls conn.CheckEnded, which removes a file", f)
		}
	}
}
