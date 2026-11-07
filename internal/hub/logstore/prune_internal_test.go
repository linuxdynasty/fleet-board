package logstore

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// TestDeleteSessionSkipsReopenedStream is the select-then-delete race:
// prune picks a closed session in a read transaction, a chunk lands
// before the delete transaction and reopens a stream, and the delete
// must see the reopened stream and skip instead of removing live data.
// Ingest stores continuations past a final chunk with no ENDED check,
// so the interleave is reachable through the public API.
func TestDeleteSessionSkipsReopenedStream(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := Open(ctx, Options{
		Dir: t.TempDir(),
		Resolve: func(p Pane) (string, string, bool) {
			return "", "", false
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	pane := Pane{Server: "default", Start: "s1", ID: "%3"}
	now := time.Now().UTC()
	encode := func(raw []byte) []byte {
		var buf bytes.Buffer
		w, err := zstd.NewWriter(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	first := []byte("first\n")
	if _, v, err := st.Ingest(ctx, Chunk{
		Session: "s", Machine: "m", Pane: pane, Stream: TerminalStream,
		Offset: 0, Data: encode(first), RawLength: uint32(len(first)), //nolint:gosec // G115: test chunks are a few bytes
		FrameStart: true, FirstTime: now.Add(-time.Hour), LastTime: now.Add(-time.Hour),
		Final: true,
	}); err != nil || v != Stored {
		t.Fatalf("ingest first v=%v err=%v", v, err)
	}
	if err := st.EndSession(ctx, "s", "m", now.Add(-2*time.Hour), nil); err != nil {
		t.Fatal(err)
	}

	candidate, _, err := st.nextCandidate(ctx, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if candidate != "s" {
		t.Fatalf("candidate = %q, want the closed session", candidate)
	}

	// A chunk lands between selection and delete and reopens the stream.
	second := []byte("second\n")
	if _, v, err := st.Ingest(ctx, Chunk{
		Session: "s", Machine: "m", Pane: pane, Stream: TerminalStream,
		Offset: uint64(len(first)), Data: encode(second), RawLength: uint32(len(second)), //nolint:gosec // G115: test chunks are a few bytes
		FrameStart: true, FrameRawOffset: uint64(len(first)),
		FirstTime: now, LastTime: now,
	}); err != nil || v != Stored {
		t.Fatalf("ingest reopen v=%v err=%v", v, err)
	}

	freed, deleted, err := st.deleteSession(ctx, candidate)
	if err != nil {
		t.Fatalf("deleteSession: %v", err)
	}
	if deleted || freed != 0 {
		t.Fatalf("deleteSession deleted=%v freed=%d, want a skip that frees nothing", deleted, freed)
	}
	spans, err := st.Read(ctx, "s", pane, TerminalStream, 0, 0)
	if err != nil {
		t.Fatalf("Read after skipped delete: %v (the reopened session must survive)", err)
	}
	// The contiguous frames merge into one span; what matters is both
	// frames survived the skipped delete byte-exact.
	if len(spans) != 1 || string(spans[0].Data) != "first\nsecond\n" {
		t.Fatalf("spans = %+v, want both frames intact", spans)
	}
}
