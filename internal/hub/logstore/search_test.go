package logstore_test

import (
	"context"
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
