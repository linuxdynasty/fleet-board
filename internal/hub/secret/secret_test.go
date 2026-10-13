package secret_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
)

func TestEnsureCreatesOnce(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	created, err := secret.Ensure(dir, rand.Read)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !created {
		t.Fatal("first ensure did not create")
	}
	fi, err := os.Stat(secret.Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", fi.Mode().Perm())
	}
	shown, err := secret.Reveal(dir)
	if err != nil {
		t.Fatalf("reveal: %v", err)
	}
	if len(shown) != 2*secret.SecretLen {
		t.Fatalf("revealed %d hex chars, want %d", len(shown), 2*secret.SecretLen)
	}
	// A second ensure never replaces the secret.
	created, err = secret.Ensure(dir, rand.Read)
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if created {
		t.Fatal("second ensure recreated the secret")
	}
	again, err := secret.Reveal(dir)
	if err != nil {
		t.Fatalf("reveal again: %v", err)
	}
	if again != shown {
		t.Fatal("the secret changed under an existing file")
	}
}

func TestCheck(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := secret.Ensure(dir, rand.Read); err != nil {
		t.Fatal(err)
	}
	shown, err := secret.Reveal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := secret.Check(dir, shown); err != nil {
		t.Fatalf("right secret refused: %v", err)
	}
	wrong := make([]byte, secret.SecretLen)
	if _, err := rand.Read(wrong); err != nil {
		t.Fatal(err)
	}
	if err := secret.Check(dir, hex.EncodeToString(wrong)); !errors.Is(err, secret.ErrRefused) {
		t.Fatalf("wrong secret = %v, want ErrRefused", err)
	}
	for _, bad := range []string{"", "zz", shown[:len(shown)-2]} {
		if err := secret.Check(dir, bad); !errors.Is(err, secret.ErrRefused) {
			t.Fatalf("presented %q = %v, want ErrRefused", bad, err)
		}
	}
	if err := secret.Check(t.TempDir(), shown); !errors.Is(err, secret.ErrMissing) {
		t.Fatalf("missing file = %v, want ErrMissing", err)
	}
}

func TestEnsureRefusesLooseFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := secret.Ensure(dir, rand.Read); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secret.Path(dir), 0o640); err != nil { //nolint:gosec // G302: the test deliberately loosens the file to prove Ensure refuses it
		t.Fatal(err)
	}
	if _, err := secret.Ensure(dir, rand.Read); err == nil {
		t.Fatal("loose secret file accepted")
	}
}
