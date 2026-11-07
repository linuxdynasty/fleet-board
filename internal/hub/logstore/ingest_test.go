package logstore_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/linuxdynasty/fleet-board/internal/hub/logstore"
)

// frame encodes raw as one zstd frame, the way the agent's chunker does:
// one frame per stream, flushed per chunk.
func frame(t *testing.T, raws ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range raws {
		if _, err := w.Write(r); err != nil {
			t.Fatal(err)
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// split cuts encoded into per-chunk data the way the store keeps it: the
// zstd blocks of one frame as received, one row per chunk.
func split(t *testing.T, encoded []byte, cuts ...int) [][]byte {
	t.Helper()
	var out [][]byte
	prev := 0
	for _, c := range cuts {
		if c > len(encoded) {
			t.Fatalf("cut %d past %d encoded bytes", c, len(encoded))
		}
		out = append(out, encoded[prev:c])
		prev = c
	}
	return append(out, encoded[prev:])
}

func testStore(t *testing.T) (*logstore.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	st, err := logstore.Open(ctx, logstore.Options{
		Dir: t.TempDir(),
		Resolve: func(p logstore.Pane) (string, string, bool) {
			return "sess-1", "machine-1", true
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
	return st, ctx
}

var pane = logstore.Pane{Server: "default", Start: "s1", ID: "%3"}

// TestReplayEqualsCapturedBytes is the row-25 acceptance core: chunks stored
// as received decode to exactly the captured raw bytes, across frame and
// chunk boundaries.
func TestReplayEqualsCapturedBytes(t *testing.T) {
	t.Parallel()
	st, ctx := testStore(t)
	first := []byte("hello terminal \x1b[32mgreen\x1b[0m\n")
	second := []byte("second line with \xe2\x9c\x93 unicode\n")
	third := []byte("third chunk after a while\n")
	encoded := frame(t, first, second, third)
	parts := split(t, encoded, len(encoded)/2)

	chunks := []logstore.Chunk{
		{
			Pane: pane, Stream: logstore.TerminalStream, Offset: 0, Data: parts[0],
			RawLength: uint32(len(first) + len(second)), FrameStart: true, FrameRawOffset: 0, //nolint:gosec // G115: test bytes are tens of bytes
			FirstTime: time.Unix(100, 0).UTC(), LastTime: time.Unix(101, 0).UTC(),
		},
		{
			Pane: pane, Stream: logstore.TerminalStream, Offset: uint64(len(first) + len(second)), Data: parts[1],
			RawLength: uint32(len(third)), FrameStart: false, FrameRawOffset: 0, //nolint:gosec // G115: test bytes are tens of bytes
			FirstTime: time.Unix(102, 0).UTC(), LastTime: time.Unix(103, 0).UTC(), Final: true,
		},
	}
	for i, c := range chunks {
		end, v, err := st.Ingest(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if v != logstore.Stored {
			t.Fatalf("chunk %d: verdict %v, want Stored", i, v)
		}
		_ = end
	}

	spans, err := st.Read(ctx, "sess-1", pane, logstore.TerminalStream, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	for _, s := range spans {
		got = append(got, s.Data...)
	}
	want := append(append(append([]byte{}, first...), second...), third...)
	if !bytes.Equal(got, want) {
		t.Fatalf("replay got %q, want %q", got, want)
	}
	if !spans[len(spans)-1].Final {
		t.Fatal("last span is not final")
	}
}

// TestDuplicateFinalClosesStream: the pane-exit final marker arrives as a
// zero-byte chunk after its bytes already flushed. It verdicts Duplicate,
// but it must still close the stream: otherwise Delete and prune treat the
// ended stream as open forever and the bytes are never reclaimed.
func TestDuplicateFinalClosesStream(t *testing.T) {
	t.Parallel()
	st, ctx := testStore(t)
	end := time.Unix(4000, 0).UTC()
	raw := []byte("hello\n")
	at, v, err := st.Ingest(ctx, logstore.Chunk{
		Session: "s-dup", Machine: "m-1",
		Pane: pane, Stream: logstore.TerminalStream, Offset: 0, Data: frame(t, raw),
		RawLength: uint32(len(raw)), FrameStart: true, //nolint:gosec // G115: test bytes are tens of bytes
		FirstTime: end.Add(-time.Second), LastTime: end,
	})
	if err != nil || v != logstore.Stored || at != uint64(len(raw)) {
		t.Fatalf("ingest end=%d v=%v err=%v", at, v, err)
	}
	at, v, err = st.Ingest(ctx, logstore.Chunk{
		Session: "s-dup", Machine: "m-1",
		Pane: pane, Stream: logstore.TerminalStream, Offset: uint64(len(raw)),
		Final: true, FirstTime: end, LastTime: end,
	})
	if err != nil || v != logstore.Duplicate || at != uint64(len(raw)) {
		t.Fatalf("final marker end=%d v=%v err=%v, want a Duplicate at 6", at, v, err)
	}
	if _, err := st.Delete(ctx, "s-dup"); err != nil {
		t.Fatalf("Delete after duplicate final: %v", err)
	}
}

// TestWrappingGapBeforeIsAHole: the accept rule is integer arithmetic, so
// a crafted GapBefore that satisfies the check only by wrapping mod 2^64
// verdicts Hole, leaving the end and the session totals untouched.
func TestWrappingGapBeforeIsAHole(t *testing.T) {
	t.Parallel()
	st, ctx := testStore(t)
	end := time.Unix(4000, 0).UTC()
	raw := []byte("hello\n")
	if _, v, err := st.Ingest(ctx, logstore.Chunk{
		Session: "s-wrap", Machine: "m-1",
		Pane: pane, Stream: logstore.TerminalStream, Offset: 0, Data: frame(t, raw),
		RawLength: uint32(len(raw)), FrameStart: true, //nolint:gosec // G115: test bytes are tens of bytes
		FirstTime: end.Add(-time.Second), LastTime: end,
	}); err != nil || v != logstore.Stored {
		t.Fatalf("ingest v=%v err=%v", v, err)
	}
	// The stored end is 6: GapBefore 2^64-2 satisfies 6+gap == 4 only by
	// wrapping, so the check must refuse it before any comparison wraps.
	evil := []byte("0123456789")
	_, v, err := st.Ingest(ctx, logstore.Chunk{
		Session: "s-wrap", Machine: "m-1",
		Pane: pane, Stream: logstore.TerminalStream, Offset: 4, Data: frame(t, evil),
		RawLength: uint32(len(evil)), FrameStart: true, FrameRawOffset: 4, //nolint:gosec // G115: test bytes are tens of bytes
		GapBefore: ^uint64(0) - 1,
		FirstTime: end, LastTime: end,
	})
	if err != nil || v != logstore.Hole {
		t.Fatalf("wrapping gap v=%v err=%v, want Hole without error", v, err)
	}
	// The end did not move: the honest continuation still stores at 6,
	// and the totals count only real bytes.
	more := []byte("world\n")
	at, v, err := st.Ingest(ctx, logstore.Chunk{
		Session: "s-wrap", Machine: "m-1",
		Pane: pane, Stream: logstore.TerminalStream, Offset: 6, Data: frame(t, more),
		RawLength: uint32(len(more)), FrameStart: true, FrameRawOffset: 6, //nolint:gosec // G115: test bytes are tens of bytes
		FirstTime: end, LastTime: end, Final: true,
	})
	if err != nil || v != logstore.Stored || at != 12 {
		t.Fatalf("continuation end=%d v=%v err=%v, want Stored at 12", at, v, err)
	}
	got, err := st.Get(ctx, "s-wrap")
	if err != nil {
		t.Fatal(err)
	}
	if got.RawBytes != 12 {
		t.Fatalf("raw bytes %d, want 12: the hole inflated nothing", got.RawBytes)
	}
}
