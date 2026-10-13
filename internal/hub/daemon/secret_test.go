package daemon_test

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
)

// TestInitCreatesTheApprovalSecret: fleetd init creates the local approval
// secret beside state.db (mode 0600) and shows it once at the terminal.
func TestInitCreatesTheApprovalSecret(t *testing.T) {
	t.Parallel()
	h := newHub(t, "")
	var shown bytes.Buffer
	opts := h.options()
	opts.SecretOutput = &shown
	if err := daemon.Init(context.Background(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	fi, err := os.Stat(secret.Path(h.state))
	if err != nil {
		t.Fatalf("no secret file: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %o, want 0600", fi.Mode().Perm())
	}
	hex64 := regexp.MustCompile(`[0-9a-f]{64}`)
	if !hex64.MatchString(shown.String()) {
		t.Fatalf("init output names no 64-hex secret: %q", shown.String())
	}
	// The shown secret is the stored one.
	revealed, err := secret.Reveal(h.state)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(shown.Bytes(), []byte(revealed)) {
		t.Fatal("init showed another secret than the stored one")
	}
}
