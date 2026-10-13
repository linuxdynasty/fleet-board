package ring_test

import (
	"context"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func TestObservedEpisodes(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	ctx := context.Background()

	live, err := db.LiveEpisodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("episodes = %d, want none on a fresh db", len(live))
	}
	ep := ring.Episode{ID: "obs-abc", Path: "/repos/a/.claude/active-goal.s1", FirstSeen: 1000}
	if err := db.SaveEpisode(ctx, ep); err != nil {
		t.Fatal(err)
	}
	// Saving the same episode again is idempotent (a replayed scan).
	if err := db.SaveEpisode(ctx, ep); err != nil {
		t.Fatal(err)
	}
	live, err = db.LiveEpisodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0] != ep {
		t.Fatalf("episodes = %+v, want [%+v]", live, ep)
	}
	if err := db.EndEpisode(ctx, ep.ID); err != nil {
		t.Fatal(err)
	}
	live, err = db.LiveEpisodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Fatalf("episodes = %+v, want none after the end", live)
	}
	// A new episode for the same file after DONE is a new row (02h: a new
	// first-seen time mints a new id).
	ep2 := ring.Episode{ID: "obs-def", Path: ep.Path, FirstSeen: 2000}
	if err := db.SaveEpisode(ctx, ep2); err != nil {
		t.Fatal(err)
	}
	live, err = db.LiveEpisodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0] != ep2 {
		t.Fatalf("episodes = %+v, want [%+v]", live, ep2)
	}
}
