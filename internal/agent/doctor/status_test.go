package doctor_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/doctor"
	"github.com/linuxdynasty/fleet-board/internal/agent/statusfile"
)

func TestStatusFile(t *testing.T) {
	t.Parallel()
	t.Run("fresh", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(12*time.Second, nil)
		r.want("status_file", doctor.OK, "12s ago")
	})
	t.Run("missing is unknown, not zero", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("status_file", doctor.Unknown, "no status.json", "fleet-agent run")
	})
	t.Run("old: the agent is down, doctor still works", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(3*time.Hour, nil)
		r.want("status_file", doctor.Warn, "3h0m0s ago", "not running", "agent.log")
	})
	t.Run("just inside two minutes is fresh", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(119*time.Second, nil)
		r.want("status_file", doctor.OK)
		r.writeStatus(121*time.Second, nil)
		r.want("status_file", doctor.Warn)
	})
	t.Run("damaged", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.write(r.state, "status.json", "{nope", 0o600)
		r.want("status_file", doctor.Warn, "damaged")
	})
	t.Run("another layout version", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.write(r.state, "status.json", `{"version":9,"written_time":"2026-10-11T11:59:00Z"}`, 0o600)
		r.want("status_file", doctor.Unknown, "another version")
	})
	t.Run("stamped in the future (a clock set back)", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(-time.Hour, nil)
		r.want("status_file", doctor.OK, "0s ago")
	})
}

func TestConnection(t *testing.T) {
	t.Parallel()
	state := func(s string) func(*statusfile.Status) {
		return func(st *statusfile.Status) { st.State = s }
	}
	t.Run("connected", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, func(s *statusfile.Status) { s.State, s.Peer = "connected", "hub.local:7443" })
		r.want("connection", doctor.OK, "connected", "hub.local:7443")
	})
	t.Run("backoff names the last failure per address", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		tm := now.Add(-time.Minute)
		r.writeStatus(time.Second, func(s *statusfile.Status) {
			s.State = "backoff"
			s.Failures = map[string]statusfile.Failure{"10.0.0.5:7443": {Step: "dial", ErrKind: "timeout", Time: &tm}}
		})
		r.want("connection", doctor.Warn, "backoff", "10.0.0.5:7443", "dial", "timeout", "--hub")
	})
	t.Run("a long failure list is cut to three", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, func(s *statusfile.Status) {
			s.State = "backoff"
			s.Failures = map[string]statusfile.Failure{}
			for _, a := range []string{"a.test:1", "b.test:1", "c.test:1", "d.test:1", "e.test:1"} {
				s.Failures[a] = statusfile.Failure{Step: "dial", ErrKind: "timeout"}
			}
		})
		c := r.get("connection")
		if n := strings.Count(c.Reason, "dial/timeout"); n != 3 || !strings.Contains(c.Reason, "2 more") {
			t.Errorf("reason = %q, want three failures and '2 more'", c.Reason)
		}
	})
	endings := []struct{ end, words string }{
		{"revoked", "new token"},
		{"unknown_machine", "new token"},
		{"fleet_mismatch", "another fleet"},
		{"cert_expired", "re-enroll"},
	}
	for _, e := range endings {
		t.Run("ended "+e.end, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.writeStatus(time.Hour, func(s *statusfile.Status) { s.State, s.Ended = "ended", e.end })
			r.want("connection", doctor.Fail, e.end, e.words)
		})
	}
	t.Run("old status: the state is last-known, not current", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(twoHours, state("connected"))
		r.want("connection", doctor.Unknown, "last known", "connected", "not running")
	})
	t.Run("no state yet", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, nil)
		r.want("connection", doctor.Unknown, "state")
	})
	t.Run("an unknown word from a newer agent", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, state("flying"))
		r.want("connection", doctor.Unknown, "unknown")
	})
	t.Run("no status file", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("connection", doctor.Unknown, "status.json")
	})
}

func TestSnapshotSize(t *testing.T) {
	t.Parallel()
	t.Run("oversize recorded", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		tm := now.Add(-time.Hour)
		r.writeStatus(time.Second, func(s *statusfile.Status) { s.SnapshotOversizeTime = &tm })
		r.want("snapshot_size", doctor.Warn, "960 KiB", "1h0m0s ago", "upgraded")
	})
	t.Run("none", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, nil)
		r.want("snapshot_size", doctor.OK)
	})
	t.Run("no status file", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("snapshot_size", doctor.Unknown)
	})
}

func TestClock(t *testing.T) {
	t.Parallel()
	offset := func(d time.Duration) func(*statusfile.Status) {
		return func(s *statusfile.Status) {
			s.Clock = &statusfile.Clock{OffsetMS: d.Milliseconds(), Time: now.Add(-10 * time.Minute)}
		}
	}
	tests := []struct {
		name   string
		off    time.Duration
		status doctor.Status
	}{
		{"in step", 800 * time.Millisecond, doctor.OK},
		{"just under 5 minutes", 5*time.Minute - time.Second, doctor.OK},
		{"over 5 minutes ahead", 6 * time.Minute, doctor.Warn},
		{"over 5 minutes behind", -6 * time.Minute, doctor.Warn},
		{"over 15 minutes", 16 * time.Minute, doctor.Fail},
		{"over 15 minutes behind", -16 * time.Minute, doctor.Fail},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.writeStatus(time.Second, offset(tc.off))
			r.want("clock", tc.status, "10m0s ago")
		})
	}
	t.Run("the sign says who is ahead", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, offset(-6*time.Minute))
		r.want("clock", doctor.Warn, "this machine is ahead")
		r.writeStatus(time.Second, offset(6*time.Minute))
		r.want("clock", doctor.Warn, "this machine is behind")
	})
	t.Run("never measured is unknown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(time.Second, nil)
		r.want("clock", doctor.Unknown, "not been measured")
	})
	t.Run("an old status file still shows the old measurement", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.writeStatus(twoHours, offset(time.Second))
		r.want("clock", doctor.OK)
	})
}

func TestEnrollFailure(t *testing.T) {
	t.Parallel()
	write := func(r *rig, e statusfile.Enroll) {
		e.Version = 1
		data, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		r.write(r.state, "enroll.json", string(data), 0o600)
	}
	t.Run("none recorded", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.want("enroll", doctor.OK, "no failed enroll")
	})
	codes := map[string]string{
		"refused": "new token", "hub_untrusted": "--hub", "bad_response": "again", "hub_failed": "again",
		"loose_mode": "chmod", "token_malformed": "whole token", "no_hub": "--hub", "hub_address": "--hub",
		"no_hub_found": "firewall", "timeout": "--hub", "interrupted": "again", "other": "enroll printed",
	}
	for code, words := range codes {
		t.Run(code, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			write(r, statusfile.Enroll{WrittenTime: now.Add(-90 * time.Minute), Code: code})
			r.want("enroll", doctor.Fail, code, "1h30m0s ago", words)
		})
	}
	t.Run("the hub's reason is shown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		write(r, statusfile.Enroll{WrittenTime: now.Add(-time.Minute), Code: "refused", Reason: "ENROLL_TOKEN_EXPIRED"})
		r.want("enroll", doctor.Fail, "ENROLL_TOKEN_EXPIRED")
	})
	t.Run("a code from a newer agent is cleaned to unknown", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		write(r, statusfile.Enroll{WrittenTime: now.Add(-time.Minute), Code: "brand_new\x1b[2J"})
		c := r.want("enroll", doctor.Fail, "unknown")
		if strings.ContainsRune(c.Reason, 0x1b) {
			t.Errorf("escape in %q", c.Reason)
		}
	})
	t.Run("a damaged file", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		r.write(r.state, "enroll.json", "[", 0o600)
		r.want("enroll", doctor.Warn, "damaged")
	})
	t.Run("a folder is not read as a file", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		if err := os.Mkdir(r.state+"/enroll.json", 0o700); err != nil {
			t.Fatal(err)
		}
		r.want("enroll", doctor.Warn, "damaged")
	})
}
