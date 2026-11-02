package logship_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/linuxdynasty/fleet-board/internal/agent/logship"
)

func frameOf(raw []byte) []byte {
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		panic(err)
	}
	if _, err := w.Write(raw); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func openSpool(t *testing.T, capBytes int64) *logship.Spool {
	t.Helper()
	sp, err := logship.OpenSpool(filepath.Join(t.TempDir(), "spool"), "default", "s1", "%3",
		logship.TerminalStream, capBytes)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

func storedAt(offset uint64, raw string, final bool) logship.StoredChunk {
	return logship.StoredChunk{
		Offset: offset, Data: frameOf([]byte(raw)), RawLength: uint32(len(raw)), //nolint:gosec // G115: test bytes are tens of bytes
		FrameStart: offset == 0, FrameRawOffset: 0,
		FirstTime: time.Unix(100, 0).UTC().UnixNano(), LastTime: time.Unix(101, 0).UTC().UnixNano(),
		Final: final,
	}
}

func TestSpoolDrainsOnAck(t *testing.T) {
	t.Parallel()
	sp := openSpool(t, 1<<30)
	if _, err := sp.Append(storedAt(0, "first\n", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Append(storedAt(6, "second\n", true)); err != nil {
		t.Fatal(err)
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending %d, want 2", len(pending))
	}
	// Acking the first chunk's end drops it; the rest stays for resend.
	if err := sp.SetAcked(6); err != nil {
		t.Fatal(err)
	}
	pending, err = sp.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Offset != 6 || !pending[0].Final {
		t.Fatalf("pending %+v, want the final chunk at 6", pending)
	}
	if err := sp.SetAcked(13); err != nil {
		t.Fatal(err)
	}
	pending, err = sp.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending %d after full ack, want 0", len(pending))
	}
	floor, err := sp.Floor()
	if err != nil {
		t.Fatal(err)
	}
	if floor != 13 {
		t.Fatalf("floor %d, want 13", floor)
	}
}

func TestSpoolSurvivesRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	open := func() *logship.Spool {
		sp, err := logship.OpenSpool(dir, "default", "s1", "%3", logship.TerminalStream, 1<<30)
		if err != nil {
			t.Fatal(err)
		}
		return sp
	}
	sp := open()
	if _, err := sp.Append(storedAt(0, "first\n", false)); err != nil {
		t.Fatal(err)
	}
	if err := sp.SetAcked(6); err != nil {
		t.Fatal(err)
	}
	// Reopen: the ack and the unacked chunk after it survive.
	sp2 := open()
	if _, err := sp2.Append(storedAt(6, "second\n", true)); err != nil {
		t.Fatal(err)
	}
	pending, err := sp2.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Offset != 6 {
		t.Fatalf("pending %+v, want one chunk at 6", pending)
	}
}

func TestSpoolCapKeepsNewest(t *testing.T) {
	t.Parallel()
	sp := openSpool(t, 64)
	var dropped uint64
	for i, raw := range []string{"aaaa\n", "bbbb\n", "cccc\n", "dddd\n"} {
		d, err := sp.Append(storedAt(uint64(i*5), raw, false))
		if err != nil {
			t.Fatal(err)
		}
		dropped += d
	}
	if dropped == 0 {
		t.Fatal("cap evicted nothing")
	}
	floor, err := sp.Floor()
	if err != nil {
		t.Fatal(err)
	}
	if floor == 0 {
		t.Fatal("floor did not advance past the evicted chunks")
	}
	pending, err := sp.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 {
		t.Fatal("cap dropped everything; the newest must stay")
	}
	if pending[0].Offset != floor {
		t.Fatalf("pending starts at %d, floor %d", pending[0].Offset, floor)
	}
	// The spool dir holds no temp files and no evicted chunks.
	entries, err := os.ReadDir(sp.Dir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if len(e.Name()) < 4 || e.Name()[len(e.Name())-4:] != ".chk" {
			t.Fatalf("stray file %q in the spool dir", e.Name())
		}
	}
}

func TestOpenSpoolRefusesStartEscape(t *testing.T) {
	t.Parallel()
	starts := []string{"../..", "..", ".", "", "a/b", "a/"}
	for i, start := range starts {
		i, start := i, start
		t.Run(fmt.Sprintf("case%d", i), func(t *testing.T) {
			t.Parallel()
			if _, err := logship.OpenSpool(filepath.Join(t.TempDir(), "spool"), "default", start, "%3",
				logship.TerminalStream, 1<<30); err == nil {
				t.Fatalf("OpenSpool(start %q) succeeded, want refusal", start)
			}
		})
	}
}

func TestSpoolIgnoresTornAckWriteOnRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	open := func() *logship.Spool {
		sp, err := logship.OpenSpool(dir, "default", "s1", "%3", logship.TerminalStream, 1<<30)
		if err != nil {
			t.Fatal(err)
		}
		return sp
	}
	sp := open()
	if _, err := sp.Append(storedAt(0, "first\n", false)); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Append(storedAt(6, "second\n", true)); err != nil {
		t.Fatal(err)
	}
	if err := sp.SetAcked(6); err != nil {
		t.Fatal(err)
	}
	// A crash between the acked tmp write and its rename leaves a stray
	// tmp: reopening must clean it and keep serving the acked end.
	if err := os.WriteFile(filepath.Join(sp.Dir(), "acked.acked.tmp"), []byte("torn"), 0o600); err != nil {
		t.Fatal(err)
	}
	sp2 := open()
	pending, err := sp2.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Offset != 6 {
		t.Fatalf("pending %+v, want one chunk at 6", pending)
	}
	floor, err := sp2.Floor()
	if err != nil {
		t.Fatal(err)
	}
	if floor != 6 {
		t.Fatalf("floor %d, want 6", floor)
	}
}

func TestSpoolPause(t *testing.T) {
	t.Parallel()
	sp := openSpool(t, 1<<30)
	if sp.Paused() {
		t.Fatal("new spool starts paused")
	}
	sp.Pause()
	if !sp.Paused() {
		t.Fatal("pause did not stick")
	}
	sp.Resume()
	if sp.Paused() {
		t.Fatal("resume did not clear the pause")
	}
}
