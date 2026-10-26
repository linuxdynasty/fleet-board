package spool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalfile"
	"github.com/linuxdynasty/fleet-board/internal/agent/goalregistry"
)

// spoolVersion is the only hook-events.jsonl version hook.py writes and
// this tailer reads (hook.py HOOK_VERSION).
const spoolVersion = 1

// maxLineBytes caps one spool line: hook lines are small JSON objects,
// and an unbounded read would let a runaway writer wedge the tailer.
const maxLineBytes = 64 * 1024

// Line is one parsed hook-events.jsonl line. Pane is the bare tmux pane
// id ("%N") the hook saw: it never becomes a registry key on its own,
// so matching runs on session id first, then the named goal.
type Line struct {
	Harness string
	Kind    agentv1.HookEventKind
	Goal    string
	Session string
	Pane    string
	Event   string
	Time    time.Time
}

// errSkip marks a line the tailer drops silently (unparseable, unknown,
// or unmatched): it is not a tailer failure.
type errSkip struct{ msg string }

func (e *errSkip) Error() string { return e.msg }

func skipf(format string, args ...any) error {
	return &errSkip{msg: fmt.Sprintf(format, args...)}
}

// IsSkip reports whether err is a silent line drop.
func IsSkip(err error) bool {
	var skip *errSkip
	return errors.As(err, &skip)
}

// rawLine mirrors hook.py's spool() shapes: kind session_mapping,
// turn_end or interrupt, with goal_id, harness_session_id and pane.
type rawLine struct {
	V              int    `json:"v"`
	Time           string `json:"time"`
	Harness        string `json:"harness"`
	Event          string `json:"event"`
	Kind           string `json:"kind"`
	GoalID         string `json:"goal_id"`
	HarnessSession string `json:"harness_session_id"`
	Pane           string `json:"pane"`
	Allowed        *bool  `json:"allowed"`
	Status         string `json:"status"`
}

// ParseLine parses one spool line into a Call-ready Line. Anything the
// tailer cannot trust exactly is a silent skip: the hook fails open, and
// so does this reader.
func ParseLine(raw []byte) (Line, error) {
	if len(raw) > maxLineBytes {
		return Line{}, skipf("spool: line over %d bytes", maxLineBytes)
	}
	var rl rawLine
	if err := json.Unmarshal(raw, &rl); err != nil {
		return Line{}, skipf("spool: bad JSON: %v", err)
	}
	if rl.V != spoolVersion {
		return Line{}, skipf("spool: version %d", rl.V)
	}
	harness := goalfile.Clean(rl.Harness, 32)
	switch harness {
	case "claude", "codex", "muse", "cursor":
	default:
		return Line{}, skipf("spool: unknown harness")
	}
	kind, ok := parseKind(rl.Kind)
	if !ok {
		return Line{}, skipf("spool: unknown kind %q", rl.Kind)
	}
	goal := rl.GoalID
	if !goalfile.ValidGoalID(goal) {
		return Line{}, skipf("spool: unsafe goal id")
	}
	ts := time.Time{}
	if rl.Time != "" {
		t, err := time.Parse(time.RFC3339, rl.Time)
		if err != nil {
			return Line{}, skipf("spool: bad time")
		}
		ts = t
	}
	return Line{
		Harness: harness, Kind: kind, Goal: goal,
		Session: goalfile.Clean(rl.HarnessSession, 256),
		Pane:    goalfile.Clean(rl.Pane, 16),
		Event:   goalfile.Clean(rl.Event, 128),
		Time:    ts,
	}, nil
}

// parseKind maps hook.py's spool kinds to registry hook kinds. The
// mapping events become USER_PROMPT (which learns the session, E9);
// a spool kind the registry never sees is a skip, never a guess.
func parseKind(kind string) (agentv1.HookEventKind, bool) {
	switch kind {
	case "session_mapping":
		return agentv1.HookEventKind_HOOK_EVENT_KIND_USER_PROMPT, true
	case "turn_end":
		return agentv1.HookEventKind_HOOK_EVENT_KIND_TURN_END, true
	case "interrupt":
		return agentv1.HookEventKind_HOOK_EVENT_KIND_INTERRUPT, true
	default:
		return agentv1.HookEventKind_HOOK_EVENT_KIND_UNSPECIFIED, false
	}
}

// Lookup resolves entries for the tailer without exposing the whole
// registry: session matches bind like the hook call, goal matches cover
// lines that carry no usable session yet.
type Lookup interface {
	ByGoal(goal string) (*agentv1.GoalRegistration, bool)
	BySession(session, harness string) (*agentv1.GoalRegistration, bool)
}

// TurnEnder resolves one hook call: session learning plus the turn-end,
// stop and interrupt events (goalregistry.Registry satisfies it).
type TurnEnder interface {
	TurnEnd(ctx context.Context, call goalregistry.Call, files goalfile.Writer, alive func(uint32) bool) (goalregistry.Outcome, error)
}

// Registry is what the tailer needs: lookups plus TurnEnd.
type Registry interface {
	Lookup
	TurnEnder
}

// CallFor converts a parsed line to the registry call. Session lines
// bind by session (the hook's own match order); lines with no session
// yet bind by the named goal's entry, which supplies the full pane key
// the spool's bare %N never could. A line whose session binds a goal
// the line does not name stays silent: sessions are never rebound here.
func CallFor(line Line, reg Lookup) (goalregistry.Call, error) {
	if line.Session != "" {
		if entry, ok := reg.BySession(line.Session, line.Harness); ok {
			if entry.GetGoal().GetValue() != line.Goal {
				return goalregistry.Call{}, skipf("spool: session names another goal")
			}
			return goalregistry.Call{
				Harness: line.Harness, Kind: line.Kind,
				Pane: entry.GetPane(), Session: line.Session, Raw: line.Event,
			}, nil
		}
	}
	entry, ok := reg.ByGoal(line.Goal)
	if !ok {
		return goalregistry.Call{}, skipf("spool: no entry for goal")
	}
	return goalregistry.Call{
		Harness: line.Harness, Kind: line.Kind,
		Pane: entry.GetPane(), Session: line.Session, Raw: line.Event,
	}, nil
}

// HandleLine parses one spool line and runs it through TurnEnd: session
// mappings learn the harness session id, turn ends and interrupts emit
// HarnessHookEvent. Silent lines return a skip error, never a failure.
func HandleLine(ctx context.Context, raw []byte, reg Registry, files goalfile.Writer, alive func(uint32) bool) error {
	line, err := ParseLine(raw)
	if err != nil {
		return err
	}
	call, err := CallFor(line, reg)
	if err != nil {
		return err
	}
	_, err = reg.TurnEnd(ctx, call, files, alive)
	return err
}
