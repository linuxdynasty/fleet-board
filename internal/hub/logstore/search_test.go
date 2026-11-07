package logstore_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/logstore"
)

func TestSearch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openFor(t, nil)
	end := time.Unix(3000, 0).UTC()
	raw := []byte("deploy started\n\x1b[32mSUCCESS deploying api\x1b[0m\nnothing here\n")
	one := frame(t, raw)
	if _, v, err := st.Ingest(ctx, logstore.Chunk{
		Session: "s-1", Machine: "m-1",
		Pane: paneID("%3"), Stream: logstore.TerminalStream, Offset: 0, Data: one,
		RawLength: uint32(len(raw)), FrameStart: true, //nolint:gosec // G115: test bytes are tens of bytes
		FirstTime: end.Add(-time.Second), LastTime: end, Final: true,
	}); err != nil || v != logstore.Stored {
		t.Fatalf("ingest v=%v err=%v", v, err)
	}

	t.Run("case-insensitive match on stripped text", func(t *testing.T) {
		matches, token, scanned, err := st.Search(ctx, logstore.SearchQuery{Query: "success DEPLOYING"})
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Fatalf("matches %d, want 1", len(matches))
		}
		m := matches[0]
		if strings.Contains(m.Snippet, "\x1b") {
			t.Fatalf("snippet %q still has escapes", m.Snippet)
		}
		if !strings.Contains(m.Snippet, "SUCCESS deploying api") {
			t.Fatalf("snippet %q, want the stripped line", m.Snippet)
		}
		if token != "" {
			t.Fatalf("token %q, want empty: the whole selection scanned", token)
		}
		if scanned == 0 {
			t.Fatal("scanned zero bytes")
		}
		// The match offset replays from the matching line.
		spans, err := st.Read(ctx, "s-1", paneID("%3"), logstore.TerminalStream, m.RawOffset, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(spans[0].Data), "SUCCESS") &&
			!strings.Contains(string(spans[0].Data), "SUCCESS") {
			t.Fatalf("replay from match offset %q misses the line", spans[0].Data)
		}
	})

	t.Run("no match scans and returns empty", func(t *testing.T) {
		matches, token, scanned, err := st.Search(ctx, logstore.SearchQuery{Query: "absent"})
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 0 || token != "" || scanned == 0 {
			t.Fatalf("matches %d token %q scanned %d", len(matches), token, scanned)
		}
	})

	t.Run("query bounds are enforced", func(t *testing.T) {
		if _, _, _, err := st.Search(ctx, logstore.SearchQuery{}); err == nil {
			t.Fatal("want an error for an empty query")
		}
		if _, _, _, err := st.Search(ctx, logstore.SearchQuery{Query: strings.Repeat("x", 257)}); err == nil {
			t.Fatal("want an error for a query above 256 bytes")
		}
		if _, _, _, err := st.Search(ctx, logstore.SearchQuery{Query: "x", PageToken: "bogus"}); err == nil {
			t.Fatal("want an error for a bad search token")
		}
	})

	t.Run("match limit pages with the token", func(t *testing.T) {
		raw2 := []byte("hit one\nhit two\nhit three\n")
		if _, v, err := st.Ingest(ctx, logstore.Chunk{
			Session: "s-2", Machine: "m-1",
			Pane: paneID("%4"), Stream: logstore.TerminalStream, Offset: 0, Data: frame(t, raw2),
			RawLength: uint32(len(raw2)), FrameStart: true, //nolint:gosec // G115: test bytes are tens of bytes
			FirstTime: end, LastTime: end, Final: true,
		}); err != nil || v != logstore.Stored {
			t.Fatalf("ingest v=%v err=%v", v, err)
		}
		var all []logstore.Match
		token := ""
		for {
			matches, next, _, err := st.Search(ctx, logstore.SearchQuery{
				Query: "hit", Limit: 2, PageToken: token,
			})
			if err != nil {
				t.Fatal(err)
			}
			all = append(all, matches...)
			token = next
			if token == "" {
				break
			}
			if len(all) > 10 {
				t.Fatal("search token loops")
			}
		}
		if len(all) != 3 {
			t.Fatalf("paged matches %d, want 3", len(all))
		}
	})
}

// TestSearchStreamsAcrossBatches pins multi-batch search: one stream holds
// more chunk rows than a single fetch batch (searchBatchRows), with one
// frame straddling the batch edge. Both framed matches must surface, and
// token paging must walk across the batches to the end.
func TestSearchStreamsAcrossBatches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openFor(t, nil)
	end := time.Unix(5000, 0).UTC()
	p := paneID("%5")
	var off uint64
	putLine := func(s string) {
		t.Helper()
		raw := []byte(s)
		if _, v, err := st.Ingest(ctx, logstore.Chunk{
			Session: "s-big", Machine: "m-1", Pane: p, Stream: logstore.TerminalStream,
			Offset: off, Data: frame(t, raw),
			RawLength: uint32(len(raw)), FrameStart: true, FrameRawOffset: off, //nolint:gosec // G115: test bytes are tens of bytes
			FirstTime: end, LastTime: end,
		}); err != nil || v != logstore.Stored {
			t.Fatalf("ingest line at %d: v=%v err=%v", off, v, err)
		}
		off += uint64(len(raw))
	}
	putFrame := func(raws ...[]byte) {
		t.Helper()
		encoded := frame(t, raws...)
		third := len(encoded) / 3
		parts := split(t, encoded, third, 2*third)
		start := off
		for i, raw := range raws {
			if _, v, err := st.Ingest(ctx, logstore.Chunk{
				Session: "s-big", Machine: "m-1", Pane: p, Stream: logstore.TerminalStream,
				Offset: off, Data: parts[i],
				RawLength: uint32(len(raw)), FrameStart: i == 0, FrameRawOffset: start, //nolint:gosec // G115: test bytes are tens of bytes
				FirstTime: end, LastTime: end,
			}); err != nil || v != logstore.Stored {
				t.Fatalf("ingest frame row %d at %d: v=%v err=%v", i, off, v, err)
			}
			off += uint64(len(raw))
		}
	}
	// Rows 0..22: single-row filler frames.
	for i := 0; i < 23; i++ {
		putLine(fmt.Sprintf("filler %03d\n", i))
	}
	// Rows 23..25: one frame straddling the 25-row batch edge, with the
	// match in its last row: the first batch ends mid-frame, so the match
	// only decodes when the carry-over rows join the second batch. The
	// trailing row alone cannot decode (no frame header), so without the
	// holdback this match is missed entirely.
	putFrame([]byte("straddle one\n"), []byte("middle filler\n"), []byte("EDGE needle-line\n"))
	// Rows 26..35: more fillers with the second match past the edge.
	for i := 0; i < 10; i++ {
		if i == 5 {
			putLine("TAIL needle-line\n")
			continue
		}
		putLine(fmt.Sprintf("later %03d\n", i))
	}

	var snippets []string
	var scanned uint64
	token := ""
	for {
		matches, next, n, err := st.Search(ctx, logstore.SearchQuery{
			Query: "needle", Limit: 1, PageToken: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		scanned = n
		for _, m := range matches {
			snippets = append(snippets, m.Snippet)
		}
		token = next
		if token == "" {
			break
		}
		if len(snippets) > 10 {
			t.Fatal("search token loops")
		}
	}
	if len(snippets) != 2 || snippets[0] != "EDGE needle-line" || snippets[1] != "TAIL needle-line" {
		t.Fatalf("snippets %q, want [EDGE TAIL] across batches", snippets)
	}
	if scanned == 0 {
		t.Fatal("scanned zero bytes")
	}
}
