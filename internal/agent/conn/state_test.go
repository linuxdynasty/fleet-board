package conn_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
	"github.com/linuxdynasty/fleet-board/internal/agent/conn/conntest"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
)

// With retries on, a lost reply is recovered inside one enroll: the retry
// sends the same token, key and install_id and gets the stored answer.
func TestALostReplyIsRecoveredWithinOneEnroll(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{Wrap: conntest.DropFirstReply()})
	a := newAgent(t)
	res, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if !res.Replayed || h.Machines(t) != 1 || len(h.IssuedCerts(t)) != 1 {
		t.Errorf("replayed %v, %d machines; want the stored answer and one machine", res.Replayed, h.Machines(t))
	}
}

// agent.proto: install_id survives re-enrollment. The advice for an
// enrolled machine removes identity.json only, so agent.db and its
// install_id stay and the hub sees no rollback.
func TestReEnrollKeepsTheInstallID(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	if _, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	_, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour)))
	if !errors.Is(err, conn.ErrAlreadyEnrolled) {
		t.Fatalf("Enroll = %v, want ErrAlreadyEnrolled", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, filepath.Join(a.dir, conn.IdentityFile)) || !strings.Contains(msg, "keep agent.db") ||
		strings.Contains(msg, "remove "+a.dir+" ") {
		t.Errorf("the advice %q must name identity.json, keep agent.db, and never say to remove the folder", msg)
	}
	if err := os.Remove(filepath.Join(a.dir, conn.IdentityFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour))); err != nil {
		t.Fatalf("Enroll after following the advice: %v", err)
	}
	ids := h.InstallIDs(t)
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Errorf("install_ids at the hub %v, want the same one twice", ids)
	}
}

// A crash between saving identity.json and removing enroll.key leaves the
// key behind; the next enroll on the enrolled folder removes it.
func TestALeftoverPendingKeyIsRemoved(t *testing.T) {
	t.Parallel()
	h := conntest.StartHub(t, conntest.Options{})
	a := newAgent(t)
	res, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(a.dir, conn.PendingKeyFile)
	if err := os.WriteFile(key, []byte(res.Identity.KeyPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	sent := h.Requests()
	if _, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour))); !errors.Is(err, conn.ErrAlreadyEnrolled) {
		t.Fatalf("Enroll = %v, want ErrAlreadyEnrolled", err)
	}
	if exists(t, key) {
		t.Error("the leftover pending key is still there")
	}
	if h.Requests() != sent {
		t.Error("the hub was contacted")
	}
}

// A symlink in place of the state folder, identity.json or enroll.key is
// refused, and nothing is sent.
func TestASymlinkInTheStateFolderIsRefused(t *testing.T) {
	t.Parallel()
	private := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name string
		set  func(t *testing.T, dir string)
	}{
		{"the state folder", func(t *testing.T, dir string) {
			t.Helper()
			target := filepath.Join(t.TempDir(), "real")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, dir); err != nil {
				t.Fatal(err)
			}
		}},
		{conn.IdentityFile, func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Symlink(private(t, "{}"), filepath.Join(dir, conn.IdentityFile)); err != nil {
				t.Fatal(err)
			}
		}},
		{conn.PendingKeyFile, func(t *testing.T, dir string) {
			t.Helper()
			if err := os.Symlink(private(t, "key"), filepath.Join(dir, conn.PendingKeyFile)); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := conntest.StartHub(t, conntest.Options{})
			a := newAgent(t)
			if tc.name != "the state folder" {
				if err := conn.EnsureDir(a.dir); err != nil {
					t.Fatal(err)
				}
			}
			tc.set(t, a.dir)
			if _, err := a.enroll(t, h.Addr, h.Token(t, time.Now().Add(time.Hour))); !errors.Is(err, conn.ErrLooseMode) {
				t.Fatalf("Enroll = %v, want ErrLooseMode", err)
			}
			if h.Requests() != 0 {
				t.Error("the hub was contacted")
			}
		})
	}
}

// The hub's expected hostname is compared the hub's way (lower case, no
// trailing "." or ".local"), and a mismatch is reported.
func TestEnrollReportsAHostnameMismatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		expected, reported string
		mismatch           bool
	}{
		{"laptop", "Laptop.local.", false},
		{"laptop.local", "LAPTOP", false},
		{"laptop", "desktop", true},
		{"laptop", "", true},
		{"", "desktop", false},
	}
	for _, tc := range cases {
		t.Run(tc.expected+"/"+tc.reported, func(t *testing.T) {
			t.Parallel()
			f := &fakeHub{auth: newCA(t), tamper: func(res *agentv1.EnrollResponse, _ *ca.Authority) {
				res.ExpectedHostname = tc.expected
			}}
			addr, tok := startFakeHub(t, f)
			a := newAgent(t)
			res, err := a.enrollWith(t, addr, tok, func(o *conn.Options) {
				o.Facts = &agentv1.MachineFacts{Hostname: tc.reported}
			})
			if err != nil {
				t.Fatal(err)
			}
			if res.HostnameMismatch != tc.mismatch || res.Hostname != tc.reported {
				t.Errorf("mismatch %v for hostname %q; want %v", res.HostnameMismatch, res.Hostname, tc.mismatch)
			}
			done := a.events(t, "enroll.completed")
			if len(done) != 1 || done[0]["hostname_mismatch"] != tc.mismatch {
				t.Errorf("enroll.completed %v, want hostname_mismatch=%v", done, tc.mismatch)
			}
		})
	}
}
