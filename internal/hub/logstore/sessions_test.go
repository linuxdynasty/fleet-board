package logstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/logstore"
)

func openFor(t *testing.T, sessions map[logstore.Pane][2]string) *logstore.Store {
	t.Helper()
	ctx := context.Background()
	st, err := logstore.Open(ctx, logstore.Options{
		Dir: t.TempDir(),
		Resolve: func(p logstore.Pane) (string, string, bool) {
			if m, ok := sessions[p]; ok {
				return m[0], m[1], true
			}
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
	return st
}

func paneID(id string) logstore.Pane { return logstore.Pane{Server: "default", Start: "s1", ID: id} }

// storeEnded ingests one final chunk for the session, then ends it.
func storeEnded(t *testing.T, ctx context.Context, st *logstore.Store, session, machine string,
	p logstore.Pane, raw []byte, end time.Time,
) {
	t.Helper()
	one := frame(t, raw)
	endOff, v, err := st.Ingest(ctx, logstore.Chunk{
		Session: session, Machine: machine,
		Pane: p, Stream: logstore.TerminalStream, Offset: 0, Data: one,
		RawLength: uint32(len(raw)), FrameStart: true, //nolint:gosec // G115: test bytes are tens of bytes
		FirstTime: end.Add(-time.Second), LastTime: end, Final: true, Goal: "goal-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if v != logstore.Stored || endOff != uint64(len(raw)) {
		t.Fatalf("ingest %v end %d", v, endOff)
	}
	if err := st.EndSession(ctx, session, machine, end, []logstore.Summary{{
		Pane: p, LastLines: "last lines", LastOutputTime: end,
		ArchivedTime: end,
	}}); err != nil {
		t.Fatal(err)
	}
}

func TestEndSessionArchives(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openFor(t, map[logstore.Pane][2]string{paneID("%3"): {"sess-1", "machine-1"}})
	storeEnded(t, ctx, st, "sess-1", "machine-1", paneID("%3"), []byte("out\n"),
		time.Unix(1000, 0).UTC())

	got, err := st.Get(ctx, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ended || got.State != "ENDED" {
		t.Fatalf("state %q ended %v, want ENDED true", got.State, got.Ended)
	}
	if len(got.Streams) != 1 || got.Streams[0].Open {
		t.Fatalf("streams %+v, want one closed stream", got.Streams)
	}
	if len(got.Summaries) != 1 || got.Summaries[0].LastLines != "last lines" {
		t.Fatalf("summaries %+v, want the archived last lines", got.Summaries)
	}
	if len(got.Goals) != 1 || got.Goals[0] != "goal-1" {
		t.Fatalf("goals %q, want [goal-1]", got.Goals)
	}
	if _, err := st.Get(ctx, "missing"); !errors.Is(err, logstore.ErrNotFound) {
		t.Fatalf("err %v, want ErrNotFound", err)
	}
}

func TestDeleteOneStep(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openFor(t, map[logstore.Pane][2]string{paneID("%3"): {"sess-1", "machine-1"}})

	t.Run("open stream refuses", func(t *testing.T) {
		raw := []byte("live output\n")
		if _, v, err := st.Ingest(ctx, logstore.Chunk{
			Session: "sess-live", Machine: "machine-1",
			Pane: paneID("%3"), Stream: logstore.TerminalStream, Offset: 0, Data: frame(t, raw),
			RawLength: uint32(len(raw)), FrameStart: true, //nolint:gosec // G115: test bytes are tens of bytes
		}); err != nil || v != logstore.Stored {
			t.Fatalf("ingest v=%v err=%v", v, err)
		}
		if _, err := st.Delete(ctx, "sess-live"); !errors.Is(err, logstore.ErrStreamOpen) {
			t.Fatalf("err %v, want ErrStreamOpen", err)
		}
	})

	t.Run("one session deleted in one step", func(t *testing.T) {
		storeEnded(t, ctx, st, "sess-1", "machine-1", paneID("%3"), []byte("out\n"),
			time.Unix(1000, 0).UTC())
		freed, err := st.Delete(ctx, "sess-1")
		if err != nil {
			t.Fatal(err)
		}
		if freed == 0 {
			t.Fatal("freed zero bytes")
		}
		if _, err := st.Get(ctx, "sess-1"); !errors.Is(err, logstore.ErrNotFound) {
			t.Fatalf("get after delete err %v, want ErrNotFound", err)
		}
		got, token, err := st.List(ctx, logstore.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range got {
			if s.ID == "sess-1" {
				t.Fatal("deleted session still listed")
			}
		}
		if token != "" {
			t.Fatalf("token %q, want empty", token)
		}
		if _, err := st.Delete(ctx, "sess-1"); !errors.Is(err, logstore.ErrNotFound) {
			t.Fatalf("second delete err %v, want ErrNotFound", err)
		}
	})
}

func TestListPagingAndFilter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openFor(t, nil)
	base := time.Unix(2000, 0).UTC()
	for i, id := range []string{"a", "b", "c"} {
		end := base.Add(time.Duration(i) * time.Minute)
		p := paneID("%" + string(rune('3'+i)))
		raw := []byte("out " + id + "\n")
		if _, _, err := st.Ingest(ctx, logstore.Chunk{
			Session: "s-" + id, Machine: "m-1",
			Pane: p, Stream: logstore.TerminalStream, Offset: 0, Data: frame(t, raw),
			RawLength: uint32(len(raw)), FrameStart: true, //nolint:gosec // G115: test bytes are tens of bytes
			FirstTime: end.Add(-time.Second), LastTime: end, Final: true,
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.EndSession(ctx, "s-"+id, "m-1", end, nil); err != nil {
			t.Fatal(err)
		}
	}

	page1, token, err := st.List(ctx, logstore.Filter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page1) != 2 || token == "" {
		t.Fatalf("page1 %d token %q, want 2 and a token", len(page1), token)
	}
	// Newest last_time first: c, then b.
	if page1[0].ID != "s-c" || page1[1].ID != "s-b" {
		t.Fatalf("page1 order %q %q, want s-c s-b", page1[0].ID, page1[1].ID)
	}
	page2, token2, err := st.List(ctx, logstore.Filter{Limit: 2, PageToken: token})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2) != 1 || page2[0].ID != "s-a" || token2 != "" {
		t.Fatalf("page2 %+v token %q, want [s-a] and empty", page2, token2)
	}
	if _, _, err := st.List(ctx, logstore.Filter{PageToken: "bogus"}); err == nil {
		t.Fatal("want an error for a bad page token")
	}
	ended, _, err := st.List(ctx, logstore.Filter{State: "ENDED"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ended) != 3 {
		t.Fatalf("ended filter got %d, want 3", len(ended))
	}
	live, _, err := st.List(ctx, logstore.Filter{State: "LIVE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("live filter got %d, want 0", len(live))
	}
}
