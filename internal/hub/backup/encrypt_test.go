package backup_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/linuxdynasty/fleet-board/internal/hub/backup"
)

// decrypt opens an age file with the passphrase and writes the plaintext to
// a new file in a temp folder.
func decrypt(t *testing.T, path, passphrase string) string {
	t.Helper()
	in, err := os.Open(path) //nolint:gosec // G304: a test file in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(in, id)
	if err != nil {
		t.Fatalf("decrypt %s: %v", path, err)
	}
	out := filepath.Join(t.TempDir(), "decrypted.db")
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return out
}

// Plan row 3: an encrypted copy is mode 0600 and, decrypted, opens and holds
// hub_identity. The passphrase is asked twice on the terminal; the plaintext
// intermediate is gone afterwards and the published file is not plaintext.
func TestAnEncryptedCopyDecryptsToTheStore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	term := &fakeTerminal{terminal: true, answers: []string{"correct horse", "correct horse"}}
	res, err := f.take(t, backup.Manual, term)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	want := filepath.Join(f.backups(), "manual-20261008T030000Z.db.age")
	if res.Path != want || !res.Encrypted {
		t.Errorf("result %+v, want path %s, encrypted", res, want)
	}
	if len(term.prompts) != 2 {
		t.Errorf("asked %d times, want twice: %q", len(term.prompts), term.prompts)
	}
	if got := names(t, f.backups()); len(got) != 1 || got[0] != filepath.Base(want) {
		t.Errorf("backups/ holds %q, want only %s", got, filepath.Base(want))
	}
	if m := mode(t, want); m != 0o600 {
		t.Errorf("mode %04o, want 0600", m)
	}
	raw, err := os.ReadFile(want) //nolint:gosec // G304: a test file in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("SQLite format 3")) || bytes.Contains(raw, []byte("fleet-test")) {
		t.Error("the encrypted copy holds plaintext")
	}
	assertBackupStore(t, decrypt(t, want, "correct horse"))
	if left := plaintextLeft(t, f.state); len(left) != 0 {
		t.Errorf("the state folder holds %q: the plaintext intermediate was not removed", left)
	}
	ev := f.logs.events(t, "backup.completed")
	if len(ev) != 1 || ev[0]["trigger"] != "manual" || ev[0]["encrypted"] != true || ev[0]["bytes"] != float64(len(raw)) {
		t.Errorf("backup.completed lines = %v, want one with trigger manual, encrypted true, bytes %d", ev, len(raw))
	}
	if bytes.Contains([]byte(f.logs.String()), []byte("correct horse")) {
		t.Error("the passphrase reached the log")
	}
}

// Plan row 3: --encrypt refuses a stdin that is not a terminal, with the
// real terminal check, before anything is written.
func TestEncryptRefusesAStdinThatIsNotATerminal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	if _, err := w.WriteString("secret\nsecret\n"); err != nil {
		t.Fatal(err)
	}
	var prompts bytes.Buffer
	_, err = f.take(t, backup.Manual, backup.NewTerminal(r, &prompts))
	if !errors.Is(err, backup.ErrNotTerminal) {
		t.Fatalf("Take error = %v, want ErrNotTerminal", err)
	}
	if prompts.Len() != 0 {
		t.Errorf("a prompt was shown on a non-terminal: %q", prompts.String())
	}
	if got := names(t, f.backups()); len(got) != 0 {
		t.Errorf("backups/ holds %q after the refusal", got)
	}
	ev := f.logs.events(t, "backup.failed")
	if len(ev) != 1 || ev[0]["step"] != "passphrase" || ev[0]["err_kind"] != "refused" {
		t.Errorf("backup.failed lines = %v, want one with step passphrase, err_kind refused", ev)
	}
}

func TestEncryptRefusesAnEmptyOrMismatchedPassphrase(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		answers []string
		want    error
	}{
		{"empty", []string{"", ""}, backup.ErrPassphraseEmpty},
		{"mismatch", []string{"one", "two"}, backup.ErrPassphraseMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			_, err := f.take(t, backup.Manual, &fakeTerminal{terminal: true, answers: tc.answers})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Take error = %v, want %v", err, tc.want)
			}
			if got := names(t, f.backups()); len(got) != 0 {
				t.Errorf("backups/ holds %q after the refusal", got)
			}
			ev := f.logs.events(t, "backup.failed")
			if len(ev) != 1 || ev[0]["step"] != "passphrase" || ev[0]["err_kind"] != "refused" {
				t.Errorf("backup.failed lines = %v, want one with step passphrase, err_kind refused", ev)
			}
		})
	}
}

// Plan row 3: the plaintext intermediate is removed on failure too, whether
// the copy fails its check or the encrypted file cannot be published.
func TestThePlaintextIntermediateIsRemovedOnFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, step string
		setup      func(t *testing.T, f *fixture)
		empty      bool
	}{
		{name: "verify fails", step: "verify", empty: true, setup: noIdentityStore},
		{name: "publish fails", step: "publish", setup: func(t *testing.T, f *fixture) {
			t.Helper()
			if err := os.Mkdir(f.backups(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.backups(), "manual-20261008T030000Z.db.age"), []byte("older"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			if tc.empty {
				f = emptyFixture(t)
			}
			tc.setup(t, f)
			before := names(t, f.backups())
			_, err := f.take(t, backup.Manual, &fakeTerminal{terminal: true, answers: []string{"pw", "pw"}})
			if err == nil {
				t.Fatal("Take succeeded")
			}
			if left := plaintextLeft(t, f.state); len(left) != 0 {
				t.Errorf("the state folder holds %q: the plaintext intermediate was not removed", left)
			}
			if after := names(t, f.backups()); !equal(after, before) {
				t.Errorf("backups/ changed from %q to %q", before, after)
			}
			ev := f.logs.events(t, "backup.failed")
			if len(ev) != 1 || ev[0]["step"] != tc.step {
				t.Errorf("backup.failed lines = %v, want one with step %s", ev, tc.step)
			}
		})
	}
}
