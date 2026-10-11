package ring_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
)

func TestHeldSettingsAreEmptyOnAFreshFile(t *testing.T) {
	t.Parallel()
	db := open(t, stateDir(t))
	h, ok, err := db.HeldSettings(context.Background())
	if err != nil || ok {
		t.Fatalf("HeldSettings on a fresh file: %+v, %v, %v; want none", h, ok, err)
	}
	st, err := db.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.SettingsEpoch != "" || st.SettingsGeneration != 0 {
		t.Errorf("State settings (%q, %d), want (\"\", 0)", st.SettingsEpoch, st.SettingsGeneration)
	}
}

func TestSaveSettingsKeepsTheOrderAndSurvivesAReopen(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	ctx := context.Background()
	db, err := ring.Open(ctx, dir, ring.Options{})
	if err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(1_700_000_000_000)
	first := ring.Held{Epoch: "e1", Generation: 13, Settings: []byte("s13"), Ack: []byte("a13"), PolicyHash: "h1", Applied: at}
	if err := db.SaveSettings(ctx, first); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	for _, tc := range []struct {
		name string
		h    ring.Held
		want error
	}{
		{"a lower generation in the same epoch", ring.Held{Epoch: "e1", Generation: 12, Settings: []byte("x"), Ack: []byte("x")}, ring.ErrSettingsOrder},
		{"no epoch", ring.Held{Generation: 14, Settings: []byte("x"), Ack: []byte("x")}, ring.ErrSettingsOrder},
	} {
		if err := db.SaveSettings(ctx, tc.h); !errors.Is(err, tc.want) {
			t.Errorf("%s: SaveSettings = %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again := open(t, dir)
	h, ok, err := again.HeldSettings(ctx)
	if err != nil || !ok {
		t.Fatalf("HeldSettings after a reopen: %v, %v", ok, err)
	}
	if h.Epoch != "e1" || h.Generation != 13 || !bytes.Equal(h.Settings, []byte("s13")) ||
		!bytes.Equal(h.Ack, []byte("a13")) || h.PolicyHash != "h1" || !h.Applied.Equal(at) {
		t.Errorf("held %+v, want the first save", h)
	}
	st, err := again.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.SettingsEpoch != "e1" || st.SettingsGeneration != 13 {
		t.Errorf("State settings (%q, %d), want (e1, 13)", st.SettingsEpoch, st.SettingsGeneration)
	}
	// The same generation is saved again (a restart re-merges under a new
	// policy), and a new epoch is taken whatever its generation (a restore).
	for _, h := range []ring.Held{
		{Epoch: "e1", Generation: 13, Settings: []byte("s13"), Ack: []byte("a13b"), PolicyHash: "h2", Applied: at},
		{Epoch: "e2", Generation: 2, Settings: []byte("s2"), Ack: []byte("a2"), PolicyHash: "h2", Applied: at},
	} {
		if err := again.SaveSettings(ctx, h); err != nil {
			t.Fatalf("SaveSettings(%s, %d): %v", h.Epoch, h.Generation, err)
		}
	}
	if st, _ := again.State(ctx); st.SettingsEpoch != "e2" || st.SettingsGeneration != 2 {
		t.Errorf("after a new epoch State settings (%q, %d), want (e2, 2)", st.SettingsEpoch, st.SettingsGeneration)
	}
}

// The order rule and the one row are kept by the file itself, whoever
// writes it (ring/AGENTS.md).
func TestTheSettingsRowRefusesAStepBackInTheFile(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	ctx := context.Background()
	db := open(t, dir)
	if err := db.SaveSettings(ctx, ring.Held{Epoch: "e1", Generation: 5, Settings: []byte("s"), Ack: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	f := raw(t, dir)
	for _, q := range []string{
		`UPDATE settings_held SET generation = 4 WHERE id = 1`,
		`DELETE FROM settings_held`,
		`INSERT OR REPLACE INTO settings_held(id, settings_epoch, generation, settings, ack, policy_hash, applied_time) VALUES (1, 'e1', 1, x'00', x'00', '', 0)`,
		`INSERT INTO settings_held(id, settings_epoch, generation, settings, ack, policy_hash, applied_time) VALUES (2, 'e1', 9, x'00', x'00', '', 0)`,
	} {
		if _, err := f.ExecContext(ctx, q); err == nil {
			t.Errorf("%s: no error, want the file to refuse it", q)
		}
	}
	if !tables(t, f)["settings_held_never_back"] {
		t.Error("agent.db has no settings_held_never_back trigger")
	}
}

// The normal path: the hub sends the next generation and the agent keeps all
// of it, not only the two numbers State reads. A save that left the ack or
// the policy hash stale would re-send an old ack after every restart.
func TestSaveSettingsKeepsEveryColumnOnEachUpdate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := open(t, stateDir(t))
	at := time.UnixMilli(1_700_000_000_000)
	for _, h := range []ring.Held{
		{Epoch: "e1", Generation: 13, Settings: []byte("s13"), Ack: []byte("a13"), PolicyHash: "h1", Applied: at},
		{Epoch: "e1", Generation: 14, Settings: []byte("s14"), Ack: []byte("a14"), PolicyHash: "h1", Applied: at.Add(time.Second)},
		{Epoch: "e1", Generation: 14, Settings: []byte("s14"), Ack: []byte("a14b"), PolicyHash: "h2", Applied: at.Add(2 * time.Second)},
		{Epoch: "e2", Generation: 2, Settings: []byte("s2"), Ack: []byte("a2"), PolicyHash: "h3", Applied: at.Add(3 * time.Second)},
	} {
		if err := db.SaveSettings(ctx, h); err != nil {
			t.Fatalf("SaveSettings(%s, %d): %v", h.Epoch, h.Generation, err)
		}
		got, ok, err := db.HeldSettings(ctx)
		if err != nil || !ok {
			t.Fatalf("HeldSettings after (%s, %d): %v, %v", h.Epoch, h.Generation, ok, err)
		}
		if got.Epoch != h.Epoch || got.Generation != h.Generation || !bytes.Equal(got.Settings, h.Settings) ||
			!bytes.Equal(got.Ack, h.Ack) || got.PolicyHash != h.PolicyHash || !got.Applied.Equal(h.Applied) {
			t.Errorf("after (%s, %d) held %+v, want %+v", h.Epoch, h.Generation, got, h)
		}
	}
}

// The file refuses only a step back. A step forward and the same generation
// are allowed from any writer, so the guard is not wider than the rule.
func TestTheSettingsRowAllowsAStepForwardInTheFile(t *testing.T) {
	t.Parallel()
	dir := stateDir(t)
	ctx := context.Background()
	db := open(t, dir)
	if err := db.SaveSettings(ctx, ring.Held{Epoch: "e1", Generation: 5, Settings: []byte("s"), Ack: []byte("a")}); err != nil {
		t.Fatal(err)
	}
	f := raw(t, dir)
	for _, q := range []string{
		`UPDATE settings_held SET generation = 6 WHERE id = 1`,
		`UPDATE settings_held SET generation = 6, policy_hash = 'x' WHERE id = 1`,
		`UPDATE settings_held SET settings_epoch = 'e2', generation = 1 WHERE id = 1`,
	} {
		if _, err := f.ExecContext(ctx, q); err != nil {
			t.Errorf("%s: %v, want the file to allow it", q, err)
		}
	}
}
