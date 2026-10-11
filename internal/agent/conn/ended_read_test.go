//go:build unix

package conn_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/conn"
)

// ReadEnded is the read-only half of CheckEnded, for `fleet-agent doctor`:
// it reads the record and changes nothing, not even a leftover for another
// machine, which CheckEnded removes.
func TestReadEndedReadsTheRecordAndChangesNothing(t *testing.T) {
	t.Parallel()
	for _, machine := range []string{"01J0000000000000000000MINE", "01J00000000000000000OTHER"} {
		dir := t.TempDir()
		writeEnded(t, dir, "revoked", machine)
		path := filepath.Join(dir, conn.EndedFile)
		before, err := os.ReadFile(path) //nolint:gosec // G304: a file in the test's temp folder
		if err != nil {
			t.Fatal(err)
		}
		infoBefore, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := conn.ReadEnded(dir)
		if err != nil {
			t.Fatalf("ReadEnded: %v", err)
		}
		if rec.End != conn.EndRevoked || rec.MachineID != machine || rec.Peer != "hub.example:7443" || rec.Time.IsZero() {
			t.Errorf("record = %+v", rec)
		}
		after, err := os.ReadFile(path) //nolint:gosec // G304: a file in the test's temp folder
		infoAfter, statErr := os.Stat(path)
		if err != nil || statErr != nil || !bytes.Equal(before, after) || !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
			t.Errorf("ended.json for %s changed by ReadEnded: %v %v", machine, err, statErr)
		}
	}
}

func TestReadEndedErrors(t *testing.T) {
	t.Parallel()
	t.Run("no file", func(t *testing.T) {
		t.Parallel()
		if _, err := conn.ReadEnded(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("err = %v, want not exist", err)
		}
	})
	for name, body := range map[string]string{
		"not json":         "{nope",
		"another version":  `{"version":9,"end":"revoked","machine_id":"m","peer":"p","time":"2026-10-11T11:00:00Z"}`,
		"an end not kept":  `{"version":1,"end":"cert_expired","machine_id":"m","peer":"p","time":"2026-10-11T11:00:00Z"}`,
		"no machine":       `{"version":1,"end":"revoked","machine_id":"","peer":"p","time":"2026-10-11T11:00:00Z"}`,
		"an unknown field": `{"version":1,"end":"revoked","machine_id":"m","peer":"p","time":"2026-10-11T11:00:00Z","x":1}`,
	} {
		t.Run("damaged: "+name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, conn.EndedFile), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ReadEnded(dir); !errors.Is(err, conn.ErrEndedDamaged) {
				t.Errorf("err = %v, want ErrEndedDamaged", err)
			}
			if _, err := os.Stat(filepath.Join(dir, conn.EndedFile)); err != nil {
				t.Errorf("a damaged file is left in place: %v", err)
			}
		})
	}
	t.Run("a hub address is cleaned and cut", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		long := string(bytes.Repeat([]byte("h"), 1000))
		body := `{"version":1,"end":"revoked","machine_id":"m","peer":"a\u001b[2J` + long + `","time":"2026-10-11T11:00:00Z"}`
		if err := os.WriteFile(filepath.Join(dir, conn.EndedFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		rec, err := conn.ReadEnded(dir)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.ContainsRune([]byte(rec.Peer), 0x1b) || len(rec.Peer) > 300 {
			t.Errorf("peer = %q (%d bytes)", rec.Peer, len(rec.Peer))
		}
	})
	t.Run("a FIFO named ended.json is refused, not opened", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		fifo := filepath.Join(dir, conn.EndedFile)
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := conn.ReadEnded(dir); done <- err }()
		select {
		case err := <-done:
			if err == nil || errors.Is(err, fs.ErrNotExist) {
				t.Errorf("err = %v, want a refusal", err)
			}
		case <-time.After(5 * time.Second):
			if w, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil { //nolint:gosec // G304: a FIFO in the test's temp folder
				_ = w.Close()
			}
			t.Fatal("ReadEnded blocked on a FIFO")
		}
	})
}

// CheckEnded still removes a leftover for another machine, and still
// refuses its own, through the same read.
func TestCheckEndedStillRemovesALeftoverAndRefusesItsOwn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeEnded(t, dir, "revoked", "01J00000000000000000OTHER")
	removed, err := conn.CheckEnded(dir, conn.Identity{MachineID: "01J0000000000000000000MINE"})
	if !removed || err != nil {
		t.Fatalf("CheckEnded = %v, %v, want a removed leftover", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, conn.EndedFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("leftover still there: %v", err)
	}
	writeEnded(t, dir, "revoked", "01J0000000000000000000MINE")
	var ee *conn.EndedError
	if _, err := conn.CheckEnded(dir, conn.Identity{MachineID: "01J0000000000000000000MINE"}); !errors.As(err, &ee) || ee.End != conn.EndRevoked {
		t.Errorf("err = %v, want an EndedError", err)
	}
}
