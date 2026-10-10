//go:build darwin

package conn_test

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// userImmutable is UF_IMMUTABLE (chflags uchg): the owner cannot remove
// the file until the flag is cleared.
const userImmutable = 0x2

// When renew.key cannot be removed after the new pair is saved, the
// renewal still completes; renew.failed{step=cleanup} names the file and
// says it is unremovable. (macOS only: the test makes the file immutable
// with chflags, which needs no root there.)
func TestARenewKeyThatCannotBeRemovedIsReported(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	dueHub(h)
	a := enrolled(t, h)
	key := filepath.Join(a.dir, conn.RenewKeyFile)
	t.Cleanup(func() { _ = syscall.Chflags(key, 0) })
	startLoop(t, a, func(o *conn.LoopOptions) {
		conn.SetRenewHook(o, func(step string) bool {
			if step == "after_rename" {
				if err := syscall.Chflags(key, userImmutable); err != nil {
					t.Errorf("chflags: %v", err)
				}
			}
			return false
		})
	})
	waitFor(t, 10*time.Second, "the renewal", func() bool { return len(a.events(t, "renew.completed")) == 1 })
	waitFor(t, 10*time.Second, "the cleanup failure", func() bool { return len(a.events(t, "renew.failed")) == 1 })
	if got := a.events(t, "renew.failed")[0]; got["step"] != "cleanup" || got["file"] != key || got["check"] != "unremovable" {
		t.Errorf("renew.failed = %v, want step=cleanup file=%s check=unremovable", got, key)
	}
	savedPair(t, a)
}
