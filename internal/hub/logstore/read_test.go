package logstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/logstore"
)

// TestReadSkipsHeadlessFrameAndMarksGap is the mid-frame-gap case: the
// spool's cap eviction dropped a frame's first chunk, so only its tail
// arrives (with the dropped raw bytes as the gap). Replay must serve the
// surviving frames and mark the gap, not fail the whole Read; Search
// must do the same.
func TestReadSkipsHeadlessFrameAndMarksGap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openFor(t, nil)
	end := time.Unix(3000, 0).UTC()
	pane := paneID("%3")

	full := frame(t, []byte("first line\n"))
	tail := full[len(full)/2:]
	if _, v, err := st.Ingest(ctx, logstore.Chunk{
		Session: "s-1", Machine: "m-1",
		Pane: pane, Stream: logstore.TerminalStream, Offset: 5, Data: tail,
		RawLength: 6, GapBefore: 5, FrameStart: false, FrameRawOffset: 0, //nolint:gosec // G115: test bytes are tens of bytes
		FirstTime: end.Add(-time.Second), LastTime: end,
	}); err != nil || v != logstore.Stored {
		t.Fatalf("ingest tail v=%v err=%v", v, err)
	}
	second := frame(t, []byte("second\n"))
	if _, v, err := st.Ingest(ctx, logstore.Chunk{
		Session: "s-1", Machine: "m-1",
		Pane: pane, Stream: logstore.TerminalStream, Offset: 11, Data: second,
		RawLength: 7, FrameStart: true, FrameRawOffset: 11, //nolint:gosec // G115: test bytes are tens of bytes
		FirstTime: end, LastTime: end, Final: true,
	}); err != nil || v != logstore.Stored {
		t.Fatalf("ingest second v=%v err=%v", v, err)
	}

	spans, err := st.Read(ctx, "s-1", pane, logstore.TerminalStream, 0, 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want the one surviving frame", spans)
	}
	if got := string(spans[0].Data); got != "second\n" {
		t.Errorf("span data = %q, want the surviving frame exact", got)
	}
	if spans[0].Offset != 11 || spans[0].GapBefore != 11 {
		t.Errorf("span offset = %d gap = %d, want 11 and 11 (the whole first frame is gone)", spans[0].Offset, spans[0].GapBefore)
	}

	matches, _, _, err := st.Search(ctx, logstore.SearchQuery{Query: "second"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("matches = %d, want 1 past the skipped frame", len(matches))
	}
}
