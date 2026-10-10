package conn_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// A renew.key the agent cannot use (damaged, or open to other users) is
// kept, never replaced: each check logs renew.failed at step key naming
// the file, what is wrong and the fix, and sends nothing. Once the owner
// removes it, the next check makes a new key and renews.
func TestAnUnusableRenewKeyIsReportedAndKept(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, check, fix string
		mode             os.FileMode
		data             func(t *testing.T) []byte
	}{
		{name: "damaged", check: "damaged", fix: "remove", mode: 0o600, data: func(*testing.T) []byte { return []byte("not a key\n") }},
		{name: "open to other users", check: "loose_mode", fix: "chmod 600", mode: 0o644, data: func(t *testing.T) []byte {
			t.Helper()
			_, key, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			return keyText(t, key)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := startConnectHub(t)
			a := enrolled(t, h)
			path := filepath.Join(a.dir, conn.RenewKeyFile)
			data := tc.data(t)
			writeFile(t, path, data, tc.mode)
			dueHub(h)
			r := startLoop(t, a, func(o *conn.LoopOptions) {
				o.RenewCheck = 10 * time.Millisecond
			})
			waitFor(t, 10*time.Second, "three failed checks", func() bool { return len(a.events(t, "renew.failed")) >= 3 })
			r.running(t)
			for _, e := range a.events(t, "renew.failed")[:3] {
				fix, _ := e["fix"].(string)
				if e["step"] != "key" || e["check"] != tc.check || e["file"] != path ||
					!strings.Contains(fix, tc.fix) || !strings.Contains(fix, path) {
					t.Fatalf("renew.failed = %v, want step=key check=%s file=%s and a fix naming it", e, tc.check, path)
				}
			}
			if n := len(h.renewKeys()); n != 0 {
				t.Errorf("%d RenewCert calls with an unusable %s, want 0", n, conn.RenewKeyFile)
			}
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, data) || perm(t, path) != tc.mode { //nolint:gosec // G304: the test's own temp folder
				t.Fatalf("%s was changed or replaced (%v)", conn.RenewKeyFile, err)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			waitFor(t, 10*time.Second, "the renewal once the file is gone", func() bool {
				return len(a.events(t, "renew.completed")) == 1
			})
			savedPair(t, a)
		})
	}
}
