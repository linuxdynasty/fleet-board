package conn_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// An enrolled folder keeps a renewed pair and a renewal in progress, and
// the "already enrolled" advice names both files; a new enrollment removes
// them before it saves the new identity, so an old machine's pair is never
// used with it.
func TestEnrollRemovesALeftoverRenewal(t *testing.T) {
	t.Parallel()
	h := startConnectHub(t)
	a := enrolled(t, h)
	certFile, renewKey := filepath.Join(a.dir, conn.CertFile), filepath.Join(a.dir, conn.RenewKeyFile)
	old := []byte("the old machine's pair\n")
	writeFile(t, certFile, old, 0o600)
	writeFile(t, renewKey, old, 0o600)
	_, err := a.enroll(t, h.addr, h.token)
	if !errors.Is(err, conn.ErrAlreadyEnrolled) {
		t.Fatalf("Enroll on the enrolled folder = %v, want ErrAlreadyEnrolled", err)
	}
	for _, name := range []string{conn.IdentityFile, conn.CertFile, conn.RenewKeyFile, "keep agent.db"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the advice %q does not name %s", err, name)
		}
	}
	for _, p := range []string{certFile, renewKey} {
		if data, err := os.ReadFile(p); err != nil || !bytes.Equal(data, old) { //nolint:gosec // G304: the test's own temp folder
			t.Errorf("a refused enroll changed %s (%v)", filepath.Base(p), err)
		}
	}
	if err := os.Remove(filepath.Join(a.dir, conn.IdentityFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.enroll(t, h.addr, h.token); err != nil {
		t.Fatalf("Enroll after removing identity.json: %v", err)
	}
	for _, p := range []string{certFile, renewKey} {
		if exists(t, p) {
			t.Errorf("%s is still there after a new enrollment", filepath.Base(p))
		}
	}
}
