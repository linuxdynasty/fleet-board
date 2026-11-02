package logship_test

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/linuxdynasty/fleet-board/internal/agent/logship"
)

// decodeAll decompresses the chunk data of one frame in order, the way the
// hub's replay does.
func decodeAll(t *testing.T, chunks []logship.OutChunk) []byte {
	t.Helper()
	var raw [][]byte
	for _, c := range chunks {
		raw = append(raw, c.Data)
	}
	r, err := zstd.NewReader(bytes.NewReader(bytes.Join(raw, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		// An open frame ends mid-stream: the whole blocks still decode.
		if !errors.Is(err, io.ErrUnexpectedEOF) || len(out) == 0 {
			t.Fatal(err)
		}
	}
	return out
}

func TestChunkerReplayEqualsInput(t *testing.T) {
	t.Parallel()
	c := logship.NewChunker()
	start := time.Unix(1000, 0).UTC()
	var got []logship.OutChunk
	writes := [][]byte{
		[]byte("first line\n"),
		bytes.Repeat([]byte("x"), 300<<10),
		[]byte("last line\n"),
	}
	var want []byte
	for i, w := range writes {
		now := start.Add(time.Duration(i) * time.Second)
		want = append(want, w...)
		out, err := c.Write(w, now)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, out...)
	}
	end, err := c.Finalize(start.Add(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, end...)
	if !bytes.Equal(decodeAll(t, got), want) {
		t.Fatal("decoded chunks do not equal the written bytes")
	}
	// One frame: only the first chunk opens it, and every chunk points at
	// the frame's start.
	if !got[0].FrameStart || got[0].FrameRawOffset != 0 {
		t.Fatalf("first chunk %+v, want frame start at 0", got[0])
	}
	for i, ch := range got[1:] {
		if ch.FrameStart {
			t.Fatalf("chunk %d opens a second frame", i+1)
		}
		if ch.FrameRawOffset != 0 {
			t.Fatalf("chunk %d frame offset %d, want 0", i+1, ch.FrameRawOffset)
		}
	}
	// Offsets chain without gaps or repeats.
	var at uint64
	for i, ch := range got {
		if ch.Offset != at {
			t.Fatalf("chunk %d offset %d, want %d", i, ch.Offset, at)
		}
		at += uint64(ch.RawLength)
	}
	if at != uint64(len(want)) {
		t.Fatalf("end %d, want %d", at, len(want))
	}
}

func TestChunkerFlushCadence(t *testing.T) {
	t.Parallel()
	c := logship.NewChunker()
	start := time.Unix(2000, 0).UTC()
	// 256 KiB raw pending flushes a chunk even before 10 s pass.
	out, err := c.Write(bytes.Repeat([]byte("y"), 256<<10), start)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].RawLength != 256<<10 {
		t.Fatalf("chunks %+v, want one 256 KiB chunk", out)
	}
	// A small write then a 10 s later flush ships the rest.
	first := out
	out, err = c.Write([]byte("small\n"), start)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("early flush shipped %d chunks", len(out))
	}
	out, err = c.Flush(start.Add(10 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("flush chunks %+v", out)
	}
	// The frame is still open, so the flush chunk decodes only with the
	// frame's earlier chunks.
	if string(decodeAll(t, append(first, out...))) != string(bytes.Repeat([]byte("y"), 256<<10))+"small\n" {
		t.Fatal("frame prefix does not decode to the written bytes")
	}
	// Nothing pending flushes nothing.
	out, err = c.Flush(start.Add(20 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("idle flush shipped %d chunks", len(out))
	}
}

func TestChunkerFrameClose(t *testing.T) {
	t.Parallel()
	c := logship.NewChunker()
	start := time.Unix(3000, 0).UTC()
	// 4 MiB raw closes the frame: the next chunk opens a new one.
	big := bytes.Repeat([]byte("z"), 4<<20)
	out, err := c.Write(big, start)
	if err != nil {
		t.Fatal(err)
	}
	next, err := c.Write([]byte("after\n"), start.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	// The new bytes are pending until the frame closes: Finalize ships
	// them in the chunk that opens the new frame.
	tail, err := c.Finalize(start.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	all := append(append(out, next...), tail...)
	if len(all) < 3 {
		t.Fatalf("chunks %d, want at least 3", len(all))
	}
	last := all[len(all)-1]
	if !last.FrameStart || last.FrameRawOffset != uint64(len(big)) {
		t.Fatalf("last chunk %+v, want a new frame at %d", last, len(big))
	}
	if string(decodeAll(t, tail)) != "after\n" {
		t.Fatal("second frame does not decode to the written bytes")
	}
	first := all[:len(all)-len(tail)]
	if !bytes.Equal(decodeAll(t, first), big) {
		t.Fatal("first frame does not decode to the written bytes")
	}
}
