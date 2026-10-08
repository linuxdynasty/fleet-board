package revocation_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
)

// Revoke order (02g): append and fsync the file, update the set, then
// state.db.
func TestRevokeWritesTheFileTheSetAndTheStore(t *testing.T) {
	t.Parallel()
	live := now.Add(48 * time.Hour)
	f := newFixture(t, []serialRow{{serial: "a1", notAfter: live}, {serial: "b2", notAfter: live}}, nil)
	l, err := f.load(t, now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := l.Revoke(ctx, []revocation.Entry{{Serial: "a1", NotAfter: live}}, now); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := l.Revoke(ctx, []revocation.Entry{{Serial: "b2", NotAfter: live}}, now); err != nil {
		t.Fatalf("second Revoke: %v", err)
	}
	if !l.Revoked("a1") || !l.Revoked("b2") {
		t.Error("the set misses a revoked serial")
	}
	if got, want := f.file(t), lines(header, line("a1", live), line("b2", live)); got != want {
		t.Errorf("file %q, want %q", got, want)
	}
	fi, err := os.Stat(filepath.Join(f.dir, revocation.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %04o, want 0600", fi.Mode().Perm())
	}
	if got := f.revokedInStore(t); !reflect.DeepEqual(got, []string{"a1", "b2"}) {
		t.Errorf("state.db revoked %v, want [a1 b2]", got)
	}
	again, err := f.load(t, now)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Revoked("a1") || !again.Revoked("b2") {
		t.Error("a reload lost a revocation")
	}
}

// A serial that is not lowercase hex without leading zeros would never match
// a certificate, so Revoke refuses it and writes nothing.
func TestRevokeRefusesASerialThatCannotMatch(t *testing.T) {
	t.Parallel()
	live := now.Add(48 * time.Hour)
	for _, serial := range []string{"0a1", "0", "A1", ""} {
		t.Run(serial, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, nil, ptr(header))
			l, err := f.load(t, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.Revoke(context.Background(), []revocation.Entry{{Serial: serial, NotAfter: live}}, now); err == nil {
				t.Fatalf("Revoke(%q) succeeded", serial)
			}
			if got := f.file(t); got != header {
				t.Errorf("file %q, want only the header", got)
			}
		})
	}
}

// A state.db write that fails leaves the serial revoked in the file and the
// set: revocation fails closed.
func TestRevokeFailsClosedWhenTheStoreWriteFails(t *testing.T) {
	t.Parallel()
	live := now.Add(48 * time.Hour)
	f := newFixture(t, []serialRow{{serial: "a1", notAfter: live}}, nil)
	l, err := f.load(t, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Revoke(context.Background(), []revocation.Entry{{Serial: "a1", NotAfter: live}}, now); err == nil {
		t.Fatal("Revoke succeeded with the store closed")
	}
	if !l.Revoked("a1") {
		t.Error("a1 is not revoked in memory after the store write failed")
	}
	if got, want := f.file(t), lines(header, line("a1", live)); got != want {
		t.Errorf("file %q, want %q", got, want)
	}
}

// A failed append may leave a torn line; the next revoke rewrites the whole
// file instead of appending after it.
func TestRevokeAfterAFailedFileWriteRewritesTheFile(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file, so the injected failure does not happen")
	}
	live := now.Add(48 * time.Hour)
	f := newFixture(t, nil, ptr(header))
	l, err := f.load(t, now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.dir, revocation.FileName)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := l.Revoke(ctx, []revocation.Entry{{Serial: "a1", NotAfter: live}}, now); err == nil {
		t.Fatal("Revoke succeeded with a read-only file")
	}
	if l.Revoked("a1") {
		t.Error("a1 is revoked in memory although the file write failed")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(header+"a1 2026"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.Revoke(ctx, []revocation.Entry{{Serial: "b2", NotAfter: live}}, now); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if got, want := f.file(t), lines(header, line("b2", live)); got != want {
		t.Errorf("file %q, want %q (rewritten, not appended after the torn line)", got, want)
	}
}
