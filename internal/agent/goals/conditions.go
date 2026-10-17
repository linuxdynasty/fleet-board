package goals

import (
	"context"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/harness/claude"
	"github.com/linuxdynasty/fleet-board/internal/harness/codex"
	"github.com/linuxdynasty/fleet-board/internal/harness/cursor"
	"github.com/linuxdynasty/fleet-board/internal/harness/muse"
)

// Captures reads a goal pane's visible screen. The condwire adapter in
// cmd/fleet-agent implements it with the inventory observer's CapturePane;
// tests stub it. A failure keeps the last level: a failed read never looks
// like a recovery.
type Captures interface {
	Capture(ctx context.Context, server, pane string) (string, error)
}

// condRescanInterval is how often the agent re-reads goal panes for
// harness conditions (02h "Interrupts and pauses"): fast enough that an
// interrupt at the machine reaches triage within seconds, slow enough
// that a machine with a few goal panes barely notices the captures.
const condRescanInterval = 10 * time.Second

// RescanConditionsInterval is how often the caller runs
// [Tracker.ScanConditions].
func RescanConditionsInterval() time.Duration { return condRescanInterval }

// openCondition is one showing harness condition: emitted open, listed in
// Snapshot.conditions while it shows, emitted once more with cleared when
// it leaves the screen or its goal ends.
type openCondition struct {
	pane    *typev1.PaneKey
	harness string
	kind    typev1.HarnessConditionKind
	text    string
	goalID  string
	cause   typev1.GoalPauseCause
	since   time.Time
}

// parseCondition reads a goal pane's screen with its harness's own
// condition parser (01 E12). It reports the kind and the shown line, or
// false: anything else fails cleanly and never guesses, so a usage or
// trust screen is quiet. OpenCode has no /goal and no condition parser,
// so its screens are always quiet.
func parseCondition(harness, screen string) (typev1.HarnessConditionKind, string, bool) {
	switch harness {
	case "claude":
		if c := claude.ParseCondition(screen); c.ParseOutcome == typev1.ParseOutcome_PARSE_OUTCOME_OK {
			return c.Kind, c.Text, true
		}
	case "codex":
		if c := codex.ParseCondition(screen); c.ParseOutcome == typev1.ParseOutcome_PARSE_OUTCOME_OK {
			return c.Kind, c.Text, true
		}
	case "muse":
		if c := muse.ParseCondition(screen); c.ParseOutcome == typev1.ParseOutcome_PARSE_OUTCOME_OK {
			return c.Kind, c.Text, true
		}
	case "cursor":
		if c := cursor.ParseCondition(screen); c.ParseOutcome == typev1.ParseOutcome_PARSE_OUTCOME_OK {
			return c.Kind, c.Text, true
		}
	}
	return typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_UNSPECIFIED, "", false
}

// pauseCauseFor says who paused the goal for the interrupt and stall
// kinds this slice parses (02h "Interrupts and pauses"). An interrupt
// line means a person pressed Esc or Ctrl-C at the machine; a stall line
// means the harness paused itself. FLEET_COMMAND and LIMIT arrive with
// row 18's goal control and row 24's limit wait, which know about the
// agent's own commands and open limit conditions.
func pauseCauseFor(kind typev1.HarnessConditionKind) typev1.GoalPauseCause {
	switch kind {
	case typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_INTERRUPTED:
		return typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_DEVELOPER_INPUT
	case typev1.HarnessConditionKind_HARNESS_CONDITION_KIND_GOAL_PAUSED:
		return typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_HARNESS
	default:
		return typev1.GoalPauseCause_GOAL_PAUSE_CAUSE_UNSPECIFIED
	}
}

// ScanConditions re-reads every live goal's pane screen and reports what
// changed: fleet-started registry panes join here with row 21 (their
// GoalRegistration carries the same pane, harness and goal triple this
// reads from the episode table today). A new condition emits an open HarnessCondition, a recovered
// screen or an ended goal emits the same event with cleared, and a kind
// change emits the cleared pair for the old kind plus the open one for
// the new. The first scan only primes the level: the connect snapshot
// carries it, as with GoalState. Like Scan it is not safe for concurrent
// use; the caller holds the same mutex. A nil Captures disables the
// observer and primes at once.
func (t *Tracker) ScanConditions(ctx context.Context) error {
	if t.cfg.Captures == nil {
		t.condPrimed = true
		return nil
	}
	ids := make([]string, 0, len(t.states))
	for id := range t.states {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		st := t.states[id]
		if st.GetPane() == nil {
			continue // ambiguous checkout: no single screen to read, keep the level
		}
		harness, ok := t.harness[id]
		if !ok || harness == "" {
			continue
		}
		key := st.GetPane()
		screen, err := t.cfg.Captures.Capture(ctx, key.GetServer(), key.GetPaneId())
		if err != nil {
			continue // a failed capture keeps the last level
		}
		kind, text, ok := parseCondition(harness, screen)
		if !ok {
			t.clearCondition(ctx, id)
			continue
		}
		t.openCondition(ctx, id, key, harness, kind, text)
	}
	for id := range t.cond {
		if _, live := t.states[id]; !live {
			t.clearCondition(ctx, id) // the goal ended: retire its triage item
		}
	}
	t.condPrimed = true
	return nil
}

// openCondition records a parsed condition, emitting the open event when
// it is new and the cleared-plus-open pair when the kind changed. The
// same kind showing again only refreshes the kept line: re-emitting it
// would spam triage with the same (pane, kind) item.
func (t *Tracker) openCondition(ctx context.Context, id string, key *typev1.PaneKey, harness string, kind typev1.HarnessConditionKind, text string) {
	if open, ok := t.cond[id]; ok {
		if open.kind == kind {
			open.text = text
			return
		}
		t.emitCondition(ctx, open, true)
	}
	open := &openCondition{
		pane: proto.CloneOf(key), harness: harness, kind: kind, text: text,
		goalID: id, cause: pauseCauseFor(kind), since: t.cfg.Clock(),
	}
	t.cond[id] = open
	if t.condPrimed {
		t.emitCondition(ctx, open, false)
	}
}

// clearCondition emits the cleared pair for an open condition and drops
// it. Nothing open emits nothing.
func (t *Tracker) clearCondition(ctx context.Context, id string) {
	open, ok := t.cond[id]
	if !ok {
		return
	}
	delete(t.cond, id)
	if t.condPrimed {
		t.emitCondition(ctx, open, true)
	}
}

// emitCondition sends one HarnessCondition event: the open one, or the
// same event with cleared when the screen recovered or the goal ended
// (03d: the hub retires the triage item on it, as on PaneExited).
func (t *Tracker) emitCondition(ctx context.Context, open *openCondition, cleared bool) {
	if t.cfg.Emit == nil {
		return
	}
	// An emit failure is the emitter's to count as loss (the hub asks for
	// a snapshot); it never ends the goal.
	_, _ = t.cfg.Emit.Emit(ctx, &agentv1.Event{Body: &agentv1.Event_HarnessCondition{
		HarnessCondition: &agentv1.HarnessCondition{
			Pane: proto.CloneOf(open.pane), Harness: &typev1.Harness{Name: open.harness},
			Kind: open.kind, Text: open.text, SinceTime: timestamppb.New(open.since),
			Cleared: cleared, GoalId: &typev1.GoalId{Value: open.goalID}, PauseCause: open.cause,
		},
	}})
}

// ConditionsPrimed reports whether a condition scan ran: the snapshot hook
// scans once before it fills, so the first snapshot already carries the
// conditions.
func (t *Tracker) ConditionsPrimed() bool { return t.condPrimed }

// FillConditions reports the open conditions in the snapshot, so triage
// is rebuilt at once after a reconnect even when the ring was trimmed
// (03g change #1). The list is sorted by pane and kind, so snapshots are
// deterministic.
func (t *Tracker) FillConditions(snap *agentv1.Snapshot) {
	ids := make([]string, 0, len(t.cond))
	for id := range t.cond {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := t.cond[ids[i]], t.cond[ids[j]]
		if a.pane.String() != b.pane.String() {
			return a.pane.String() < b.pane.String()
		}
		return a.kind < b.kind
	})
	for _, id := range ids {
		open := t.cond[id]
		snap.Conditions = append(snap.Conditions, &agentv1.HarnessCondition{
			Pane: proto.CloneOf(open.pane), Harness: &typev1.Harness{Name: open.harness},
			Kind: open.kind, Text: open.text, SinceTime: timestamppb.New(open.since),
			GoalId: &typev1.GoalId{Value: open.goalID}, PauseCause: open.cause,
		})
	}
}
