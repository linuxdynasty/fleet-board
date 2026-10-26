package spool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalregistry"
)

func sscanfOffset(s string, off *int) (int, error) {
	return fmt.Sscanf(s, "%d", off)
}

func TestParseLine(t *testing.T) {
	t.Parallel()
	mapping := `{"v":1,"time":"2026-10-26T00:00:00Z","harness":"claude","event":"UserPromptSubmit","kind":"session_mapping","goal_id":"goal1","harness_session_id":"sess-1","pane":"%1"}`
	turn := `{"v":1,"time":"2026-10-26T00:00:01Z","harness":"codex","event":"Stop","kind":"turn_end","allowed":true,"goal_id":"goal2","harness_session_id":"sess-2","pane":"%2"}`
	interrupt := `{"v":1,"time":"2026-10-26T00:00:02Z","harness":"muse","event":"Interrupt","kind":"interrupt","goal_id":"goal3","harness_session_id":"","pane":""}`
	cases := []struct {
		name    string
		raw     string
		want    Line
		wantErr bool
	}{
		{
			name: "session mapping", raw: mapping, want: Line{
				Harness: "claude", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT,
				Goal: "goal1", Session: "sess-1", Pane: "%1", Event: "UserPromptSubmit",
			},
		},
		{
			name: "turn end", raw: turn, want: Line{
				Harness: "codex", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END,
				Goal: "goal2", Session: "sess-2", Pane: "%2", Event: "Stop",
			},
		},
		{
			name: "interrupt", raw: interrupt, want: Line{
				Harness: "muse", Kind: agentv1.HookEventKind_HOOK_EVENT_KIND_INTERRUPT,
				Goal: "goal3", Event: "Interrupt",
			},
		},
		{name: "bad json", raw: `{"v":1,`, wantErr: true},
		{name: "wrong version", raw: `{"v":2,"harness":"claude","kind":"turn_end","goal_id":"goal1"}`, wantErr: true},
		{name: "unknown kind", raw: `{"v":1,"harness":"claude","kind":"zap","goal_id":"goal1"}`, wantErr: true},
		{name: "unknown harness", raw: `{"v":1,"harness":"gemini","kind":"turn_end","goal_id":"goal1"}`, wantErr: true},
		{name: "unsafe goal id", raw: `{"v":1,"harness":"claude","kind":"turn_end","goal_id":"../x"}`, wantErr: true},
		{name: "bad time", raw: `{"v":1,"time":"yesterday","harness":"claude","kind":"turn_end","goal_id":"goal1"}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLine([]byte(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseLine = %+v, nil; want skip", got)
				}
				if !IsSkip(err) {
					t.Fatalf("ParseLine err = %v; want a silent skip", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLine = %v", err)
			}
			if got.Harness != tc.want.Harness || got.Kind != tc.want.Kind || got.Goal != tc.want.Goal ||
				got.Session != tc.want.Session || got.Pane != tc.want.Pane || got.Event != tc.want.Event {
				t.Fatalf("ParseLine = %+v; want %+v", got, tc.want)
			}
			if tc.name == "session mapping" && got.Time.IsZero() {
				t.Fatal("parsed time is zero")
			}
		})
	}
}

func testRegistry() *goalregistry.Registry {
	return goalregistry.New(nil, time.Now)
}

func testPane(server, start, id string) *typev1.PaneKey {
	return &typev1.PaneKey{Server: server, ServerStartId: start, PaneId: id}
}

func TestCallFor(t *testing.T) {
	t.Parallel()
	reg := testRegistry()
	pane1 := testPane("default", "s1", "%1")
	pane2 := testPane("default", "s1", "%2")
	if _, err := reg.Register(pane1, "goal1", "claude", "/wt/1", false); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Register(pane2, "goal2", "claude", "/wt/2", false); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.LearnSession(pane1, "sess-1"); err != nil {
		t.Fatal(err)
	}
	t.Run("session binds its goal", func(t *testing.T) {
		t.Parallel()
		call, err := CallFor(Line{
			Harness: "claude",
			Kind:    agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END,
			Goal:    "goal1", Session: "sess-1", Event: "Stop",
		}, reg)
		if err != nil {
			t.Fatal(err)
		}
		if call.Pane.GetPaneId() != "%1" || call.Raw != "Stop" {
			t.Fatalf("call = %+v", call)
		}
	})
	t.Run("session naming another goal stays silent", func(t *testing.T) {
		t.Parallel()
		if _, err := CallFor(Line{
			Harness: "claude",
			Kind:    agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END,
			Goal:    "goal2", Session: "sess-1",
		}, reg); !IsSkip(err) {
			t.Fatalf("err = %v; want a silent skip", err)
		}
	})
	t.Run("goal binds without a session", func(t *testing.T) {
		t.Parallel()
		call, err := CallFor(Line{
			Harness: "claude",
			Kind:    agentv1.HookEventKind_HOOK_EVENT_KIND_INTERRUPT,
			Goal:    "goal2", Event: "Interrupt",
		}, reg)
		if err != nil {
			t.Fatal(err)
		}
		if call.Pane.GetPaneId() != "%2" {
			t.Fatalf("call = %+v", call)
		}
	})
	t.Run("unknown goal stays silent", func(t *testing.T) {
		t.Parallel()
		if _, err := CallFor(Line{
			Harness: "claude",
			Kind:    agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END,
			Goal:    "goal9",
		}, reg); !IsSkip(err) {
			t.Fatalf("err = %v; want a silent skip", err)
		}
	})
}

func TestHandleLineLearnsSession(t *testing.T) {
	t.Parallel()
	var events []*agentv1.Event
	emit := func(_ context.Context, ev *agentv1.Event) (uint64, error) {
		events = append(events, ev)
		return uint64(len(events)), nil
	}
	reg := goalregistry.New(emit, time.Now)
	pane := testPane("default", "s1", "%1")
	if _, err := reg.Register(pane, "goal1", "claude", t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	events = nil // the CREATED event above is setup, not the test's doing
	raw := `{"v":1,"time":"2026-10-26T00:00:00Z","harness":"claude","event":"UserPromptSubmit","kind":"session_mapping","goal_id":"goal1","harness_session_id":"sess-1","pane":"%1"}`
	files := goalfile.Writer{EnsureBoard: func(workdir string) (string, bool, error) {
		return workdir, false, nil
	}}
	if err := HandleLine(context.Background(), []byte(raw), reg, files, nil); err != nil {
		t.Fatal(err)
	}
	entry, ok := reg.BySession("sess-1", "claude")
	if !ok || entry.GetGoal().GetValue() != "goal1" {
		t.Fatalf("session not learned: %+v %v", entry, ok)
	}
	if len(events) != 1 || events[0].GetPaneHarnessSession() == nil {
		t.Fatalf("events = %+v; want one session-learned event", events)
	}
	// A replay learns nothing twice: the same line emits nothing more.
	if err := HandleLine(context.Background(), []byte(raw), reg, files, nil); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d; want no duplicate learned event", len(events))
	}
}

func TestTailerFollows(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	spool := filepath.Join(dir, "hook-events.jsonl")
	reg := testRegistry()
	pane := testPane("default", "s1", "%1")
	wt := t.TempDir()
	if _, err := reg.Register(pane, "goal1", "claude", wt, false); err != nil {
		t.Fatal(err)
	}
	files := goalfile.Writer{EnsureBoard: func(workdir string) (string, bool, error) {
		return workdir, false, nil
	}}
	tail, err := New(Config{Spool: spool, Registry: reg, Files: files, Poll: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	mapping := "{\"v\":1,\"harness\":\"claude\",\"event\":\"UserPromptSubmit\",\"kind\":\"session_mapping\",\"goal_id\":\"goal1\",\"harness_session_id\":\"sess-9\",\"pane\":\"%1\"}\n"
	junk := "{\"v\":9}\n"
	if err := os.WriteFile(spool, []byte(mapping+junk), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- tail.Run(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if entry, ok := reg.BySession("sess-9", "claude"); ok && entry.GetGoal().GetValue() == "goal1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tailer never learned the session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The offset moved past both lines, junk included: skips are judged,
	// not pending.
	raw, err := os.ReadFile(spool + ".offset") //nolint:gosec // G304: the offset file this run wrote in a temp dir
	if err != nil {
		t.Fatal(err)
	}
	var off int
	if _, err := sscanfOffset(string(raw), &off); err != nil || off != len(mapping+junk) {
		t.Fatalf("offset = %q; want %d", raw, len(mapping+junk))
	}
}
