package logstore_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/linuxdynasty/fleet-board/internal/hub/logstore"
)

// fillEnded stores ended sessions with one chunk each, oldest first. The
// raw bytes are incompressible noise, so each session spans many pages and
// deleting one moves whole pages to the freelist: the live-byte measure
// moves like it does at production scale, not in page-granularity steps.
func fillEnded(t *testing.T, ctx context.Context, st *logstore.Store, n int, base time.Time) {
	t.Helper()
	for i := 0; i < n; i++ {
		id := string(rune('a'+i)) + "-old"
		end := base.Add(time.Duration(i) * time.Minute)
		p := logstore.Pane{Server: "default", Start: "s1", ID: "%" + string(rune('1'+i))}
		raw := noise(128<<10, uint32(i+1))
		if _, v, err := st.Ingest(ctx, chunkFor(id, p, raw, end)); err != nil || v != logstore.Stored {
			t.Fatalf("ingest %s v=%v err=%v", id, v, err)
		}
		if err := st.EndSession(ctx, id, "m-1", end, nil); err != nil {
			t.Fatal(err)
		}
	}
}

// frameOf encodes one raw input as one zstd frame without a test handle.
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

// noise is deterministic incompressible bytes (an LCG): zstd stores them
// nearly raw, so the test measures pages, not compression.
func noise(n int, seed uint32) []byte {
	out := make([]byte, n)
	x := seed
	for i := range out {
		x = x*1664525 + 1013904223
		out[i] = byte(x >> 24)
	}
	return out
}

func chunkFor(id string, p logstore.Pane, raw []byte, end time.Time) logstore.Chunk {
	return logstore.Chunk{
		Session: id, Machine: "m-1",
		Pane: p, Stream: logstore.TerminalStream, Offset: 0, Data: frameOf(raw),
		RawLength: uint32(len(raw)), FrameStart: true, //nolint:gosec // G115: test chunks are 128 KiB
		FirstTime: end.Add(-time.Second), LastTime: end, Final: true,
	}
}

func TestPruneOldestFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openFor(t, nil)
	base := time.Unix(4000, 0).UTC()
	fillEnded(t, ctx, st, 4, base)

	before, err := st.Stat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A cap between one and four sessions' live bytes prunes the oldest
	// until the store fits.
	capBytes := before.UsedBytes / 2
	out, err := st.Prune(ctx, capBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Blocked != "" {
		t.Fatalf("blocked %q, want held", out.Blocked)
	}
	if out.PrunedSessions == 0 {
		t.Fatal("pruned nothing")
	}
	after, err := st.Stat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.UsedBytes > capBytes {
		t.Fatalf("live %d is above cap %d", after.UsedBytes, capBytes)
	}
	// The oldest is gone, the newest stays: prune order is end_time asc.
	if _, err := st.Get(ctx, "a-old"); err == nil {
		t.Fatal("oldest session survived the prune")
	}
	if _, err := st.Get(ctx, "d-old"); err != nil {
		t.Fatalf("newest session pruned: %v", err)
	}
	// Freed pages not yet vacuumed do not count: the file on disk can be
	// larger than the live measure.
	if after.FileBytes < after.UsedBytes {
		t.Fatalf("file %d below live %d", after.FileBytes, after.UsedBytes)
	}
	if !after.LastPrune.After(base) && after.LastPrune.IsZero() {
		t.Fatal("last prune time not stamped")
	}
}

func TestPruneBlockedOnlyOpen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openFor(t, nil)
	raw := []byte("live stream\n")
	now := time.Unix(4500, 0).UTC()
	if _, v, err := st.Ingest(ctx, logstore.Chunk{
		Session: "open-1", Machine: "m-1",
		Pane: paneID("%3"), Stream: logstore.TerminalStream, Offset: 0, Data: frame(t, raw),
		RawLength: uint32(len(raw)), FrameStart: true, Final: false, //nolint:gosec // G115: test bytes are tens of bytes
		FirstTime: now.Add(-time.Second), LastTime: now,
	}); err != nil || v != logstore.Stored {
		t.Fatalf("ingest v=%v err=%v", v, err)
	}
	stat, err := st.Stat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A cap of zero keeps everything and reports the block; output is never
	// dropped for the cap.
	out, err := st.Prune(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if out.Blocked != logstore.BlockNoFinalSession {
		t.Fatalf("blocked %q, want no_final_session", out.Blocked)
	}
	if out.PrunedSessions != 0 {
		t.Fatalf("pruned %d open sessions, want 0", out.PrunedSessions)
	}
	if stat.Sessions != 1 || stat.Streams != 1 || !stat.HasOldest {
		t.Fatalf("stat %+v, want 1 session 1 stream with oldest", stat)
	}
}

func TestPruneRetentionAge(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openFor(t, nil)
	now := time.Unix(5000, 0).UTC()
	old := now.Add(-time.Hour)
	fillEnded(t, ctx, st, 2, old)
	// A one-minute retention deletes both ended sessions even under a huge
	// cap.
	out, err := st.Prune(ctx, 1<<62, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if out.PrunedSessions != 2 {
		t.Fatalf("pruned %d, want 2 age-expired sessions", out.PrunedSessions)
	}
}
