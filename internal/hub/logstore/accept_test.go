package logstore_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/logstore"
)

// ingestChunks stores raw frames as chunks and returns the encoded form, so
// offset tests work out their expectations by hand from the raw lengths.
func ingestChunks(t *testing.T, st *logstore.Store, pane logstore.Pane, stream int, raws ...[]byte) {
	t.Helper()
	var chunks []logstore.Chunk
	at := uint64(0)
	// One frame per raw input, so offset tests work out their expectations
	// by hand from the raw lengths.
	for i, r := range raws {
		one := frame(t, r)
		chunks = append(chunks, logstore.Chunk{
			Session: "sess-1", Machine: "machine-1", Pane: pane, Stream: stream,
			Offset: at, Data: one, RawLength: uint32(len(r)), //nolint:gosec // G115: test bytes are tens of bytes
			FrameStart: true, FrameRawOffset: at,
			FirstTime: time.Unix(int64(100+i), 0).UTC(),
			LastTime:  time.Unix(int64(100+i), 0).UTC(),
		})
		at += uint64(len(r))
	}
	ctx := context.Background()
	for i, c := range chunks {
		end, v, err := st.Ingest(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if v != logstore.Stored {
			t.Fatalf("chunk %d: verdict %v, want Stored", i, v)
		}
		if end != c.Offset+uint64(c.RawLength) {
			t.Fatalf("chunk %d: end %d, want %d", i, end, c.Offset+uint64(c.RawLength))
		}
	}
}

func TestAcceptRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := logstore.Open(ctx, logstore.Options{
		Dir: t.TempDir(),
		Resolve: func(p logstore.Pane) (string, string, bool) {
			if p.ID == "%9" {
				return "", "", false
			}
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
	raw := []byte("twelve bytes")
	one := frame(t, raw)
	base := logstore.Chunk{
		Pane: pane, Stream: logstore.TerminalStream, Offset: 0,
		Data: one, RawLength: uint32(len(raw)), FrameStart: true, //nolint:gosec // G115: test bytes are tens of bytes
	}

	cases := []struct {
		name    string
		chunk   logstore.Chunk
		end     uint64
		verdict logstore.Verdict
	}{
		{"store", base, uint64(len(raw)), logstore.Stored},
		{"duplicate exact resend re-acks the end", base, uint64(len(raw)), logstore.Duplicate},
		{"chunk inside the stored span is a duplicate", func() logstore.Chunk {
			c := base
			c.RawLength = 4
			c.Data = frame(t, raw[:4])
			return c
		}(), uint64(len(raw)), logstore.Duplicate},
		{"hole past the end refuses with the hub end", func() logstore.Chunk {
			c := base
			c.Offset = uint64(len(raw)) + 10
			return c
		}(), uint64(len(raw)), logstore.Hole},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			end, v, err := st.Ingest(ctx, tc.chunk)
			if err != nil {
				t.Fatal(err)
			}
			if v != tc.verdict {
				t.Fatalf("verdict %v, want %v", v, tc.verdict)
			}
			if end != tc.end {
				t.Fatalf("end %d, want %d", end, tc.end)
			}
		})
	}

	t.Run("unknown pane keeps the spool", func(t *testing.T) {
		c := base
		c.Pane = logstore.Pane{Server: "default", Start: "s1", ID: "%9"}
		c.Session = ""
		end, v, err := st.Ingest(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if v != logstore.UnknownStream || end != 0 {
			t.Fatalf("verdict %v end %d, want UnknownStream 0", v, end)
		}
	})

	t.Run("gap counted once at its chunk", func(t *testing.T) {
		// The hub lost its tail (restored backup): the agent resends from
		// its spool floor with gap_before past the hub's end.
		c := base
		c.Offset = uint64(len(raw)) + 7
		c.GapBefore = 7
		c.Data = frame(t, []byte("more"))
		c.RawLength = 4
		end, v, err := st.Ingest(ctx, c)
		if err != nil {
			t.Fatal(err)
		}
		if v != logstore.Stored {
			t.Fatalf("verdict %v, want Stored", v)
		}
		if end != uint64(len(raw))+7+4 {
			t.Fatalf("end %d, want %d", end, uint64(len(raw))+7+4)
		}
		got, err := st.Get(ctx, "sess-1")
		if err != nil {
			t.Fatal(err)
		}
		if got.GapBytes != 7 {
			t.Fatalf("gap bytes %d, want 7", got.GapBytes)
		}
	})

	t.Run("wrapping offset is a hole, not a duplicate", func(t *testing.T) {
		// Offset=2^64-6 plus RawLength=10 wraps to 4, which sits below
		// the stored end: without a wrap guard the duplicate check
		// takes the DUPLICATE arm for a far-ahead hostile chunk.
		// The shared store sits at end 23 here; continue it to 32.
		raw9 := bytes.Repeat([]byte("x"), 9)
		if _, v, err := st.Ingest(ctx, logstore.Chunk{
			Pane: pane, Stream: logstore.TerminalStream, Offset: 23,
			Data: frame(t, raw9), RawLength: uint32(len(raw9)), FrameStart: true, //nolint:gosec // G115: test bytes are nine bytes
			FrameRawOffset: 23,
		}); err != nil || v != logstore.Stored {
			t.Fatalf("ingest 9 v=%v err=%v", v, err)
		}
		end, v, err := st.Ingest(ctx, logstore.Chunk{
			Pane: pane, Stream: logstore.TerminalStream,
			Offset: ^uint64(0) - 5, Data: frame(t, []byte("hostile")), RawLength: 10,
			FrameStart: true, FrameRawOffset: ^uint64(0) - 5,
		})
		if err != nil {
			t.Fatal(err)
		}
		if v != logstore.Hole {
			t.Fatalf("verdict %v, want Hole for the wrapping far-ahead chunk", v)
		}
		if end != 32 {
			t.Fatalf("end %d, want 32 unchanged", end)
		}
		got, err := st.Get(ctx, "sess-1")
		if err != nil {
			t.Fatal(err)
		}
		if got.RawBytes != 32 || got.GapBytes != 7 {
			t.Fatalf("raw %d gap %d, want 32 and 7 unchanged", got.RawBytes, got.GapBytes)
		}
	})

	t.Run("overlong chunk is refused", func(t *testing.T) {
		c := base
		c.Offset = 1 << 20
		c.Data = make([]byte, 960*1024+1)
		if _, _, err := st.Ingest(ctx, c); err == nil {
			t.Fatal("want an error for a chunk above 960 KiB")
		}
	})
}

func TestReadRanges(t *testing.T) {
	t.Parallel()
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
	t.Cleanup(func() { _ = st.Close() })
	a := []byte("first\n")
	b := []byte("second\n")
	c := []byte("third\n")
	ingestChunks(t, st, pane, logstore.TerminalStream, a, b, c)

	t.Run("offset read starts mid-stream", func(t *testing.T) {
		spans, err := st.Read(ctx, "sess-1", pane, logstore.TerminalStream, uint64(len(a)), 0)
		if err != nil {
			t.Fatal(err)
		}
		var got []byte
		for _, s := range spans {
			got = append(got, s.Data...)
		}
		want := append(append([]byte{}, b...), c...)
		if string(got) != string(want) {
			t.Fatalf("got %q, want %q", got, want)
		}
		if spans[0].Offset != uint64(len(a)) {
			t.Fatalf("first span offset %d, want %d", spans[0].Offset, len(a))
		}
	})

	t.Run("max bytes stops the replay", func(t *testing.T) {
		spans, err := st.Read(ctx, "sess-1", pane, logstore.TerminalStream, 0, uint64(len(a)))
		if err != nil {
			t.Fatal(err)
		}
		var n int
		for _, s := range spans {
			n += len(s.Data)
		}
		if n != len(a) {
			t.Fatalf("replayed %d bytes, want %d", n, len(a))
		}
	})

	t.Run("unknown session is not found", func(t *testing.T) {
		if _, err := st.Read(ctx, "nope", pane, logstore.TerminalStream, 0, 0); !errors.Is(err, logstore.ErrNotFound) {
			t.Fatalf("err %v, want ErrNotFound", err)
		}
	})
}
