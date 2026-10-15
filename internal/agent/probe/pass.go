package probe

import (
	"context"
	"errors"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// pass runs one probe pass: ensure the session, read the screen before
// any key, and either type the one status command or report why nothing
// was typed. It always emits ProbeHealth, and a UsageReading only when a
// usage screen was read.
func (m *Manager) pass(ctx context.Context, a *assignment) *agentv1.CommandResult {
	now := m.now()
	name := sessionName(a.probeID.GetValue())
	harnessName := a.harness.GetName()
	// The key of any pre-existing session; it is read again after the
	// session is ensured, so first-pass health names the live pane.
	pane := m.paneKey(ctx, name)

	binary, ok := harnessBinaries[harnessName]
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: no binary for harness "+harnessName)
	}
	// The resolved path launches the session, not the bare name: the
	// tmux server need not share the agent's PATH.
	looked, err := m.lookPath(binary)
	if err != nil {
		return m.failed(ctx, a, pane, now, false, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_INSTALLED, true,
			"harness "+binary+" is not on PATH")
	}
	if err := m.ensureSession(ctx, name, looked); err != nil {
		if ctx.Err() != nil {
			return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: pass ended with its context")
		}
		return m.failed(ctx, a, pane, now, false, typev1.ReasonCode_REASON_CODE_PROBE_START_FAILED, true,
			"the probe session would not start")
	}
	pane = m.paneKey(ctx, name)
	screen, settled := m.settle(ctx, name)
	if !settled {
		if ctx.Err() != nil {
			return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: pass ended with its context")
		}
		return m.failed(ctx, a, pane, now, true, typev1.ReasonCode_REASON_CODE_PROBE_COMMAND_TIMEOUT, true,
			"the probe screen never settled")
	}
	switch c := Classify(harnessName, screen); c.Outcome {
	case OutcomeTrust:
		// The command reports FOLDER_UNTRUSTED (01 C3) while health
		// reports STARTUP_PROMPT_SEEN with kind trust (03b, and the
		// pacing ProbeBlock the hub keys off): different consumers,
		// different codes, both as specified.
		return m.blocked(ctx, a, pane, now, typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED,
			typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN, "trust",
			"folder trust screen: trust the probe folder in your own terminal; nothing typed")
	case OutcomeStartup:
		return m.blocked(ctx, a, pane, now, typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN,
			typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN, c.Kind,
			kindMessage(c.Kind))
	case OutcomeReady:
		return m.runStatus(ctx, a, pane, now, name, harnessName, screen)
	default:
		return m.idleUnknown(ctx, a, pane, now, name, c.Input)
	}
}

// kindMessage names the startup screen for the result message: closed
// words only, never screen text.
func kindMessage(kind string) string {
	switch kind {
	case KindUpdate:
		return "update screen showing; nothing typed"
	case KindReleaseNotes:
		return "release notes screen showing; nothing typed"
	default:
		return "startup screen showing; nothing typed"
	}
}

// idleUnknown handles a settled screen that proves nothing: a draft in
// the input line is PANE_BUSY, a dead pane or a bare shell is
// PROBE_START_FAILED, and anything else is READY_TIMEOUT. All three type
// nothing and send health only.
func (m *Manager) idleUnknown(ctx context.Context, a *assignment, pane *typev1.PaneKey, now time.Time, name string, input Input) *agentv1.CommandResult {
	if input == InputDraft {
		return m.failed(ctx, a, pane, now, true, typev1.ReasonCode_REASON_CODE_PANE_BUSY, true,
			"the probe pane holds typed text; will retry")
	}
	dead, err := m.be.PaneDead(ctx, name)
	switch {
	case err == nil && !dead:
		cmd, cerr := m.be.PaneCommand(ctx, name)
		switch {
		case cerr == nil && !shellCommands[cmd]:
			return m.failed(ctx, a, pane, now, true, typev1.ReasonCode_REASON_CODE_READY_TIMEOUT, true,
				"the harness never reached its prompt")
		case cerr == nil:
			return m.failed(ctx, a, pane, now, false, typev1.ReasonCode_REASON_CODE_PROBE_START_FAILED, true,
				"the harness left its pane")
		case ctx.Err() != nil:
			return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: pass ended with its context")
		default:
			return m.failed(ctx, a, pane, now, false, typev1.ReasonCode_REASON_CODE_INTERNAL, false,
				"probe: its pane would not read")
		}
	case ctx.Err() != nil:
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: pass ended with its context")
	case err != nil:
		return m.failed(ctx, a, pane, now, false, typev1.ReasonCode_REASON_CODE_INTERNAL, false,
			"probe: its pane would not read")
	default:
		return m.failed(ctx, a, pane, now, false, typev1.ReasonCode_REASON_CODE_PROBE_START_FAILED, true,
			"the harness left its pane")
	}
}

// runStatus types the one status command and reads the answer. The screen
// was proven idle with an empty input line by Classify; this is the only
// place a pass sends keys, and it sends nothing else.
func (m *Manager) runStatus(ctx context.Context, a *assignment, pane *typev1.PaneKey, now time.Time, name, harnessName, before string) *agentv1.CommandResult {
	command, _ := StatusCommand(harnessName)
	if err := m.be.SendCommand(ctx, name, command); err != nil {
		return m.failed(ctx, a, pane, now, true, typev1.ReasonCode_REASON_CODE_PROBE_COMMAND_TIMEOUT, true,
			"the status command did not send")
	}
	screen, settled := m.settle(ctx, name)
	if !settled {
		return m.failed(ctx, a, pane, now, true, typev1.ReasonCode_REASON_CODE_PROBE_COMMAND_TIMEOUT, true,
			"the usage screen never settled")
	}
	reading, ok := BuildReading(harnessName, screen, m.now(), m.paneKey(ctx, name), a.probeID, a.accountKey, a.harness)
	if !ok {
		return fail(typev1.ReasonCode_REASON_CODE_INTERNAL, false, "probe: no usage parser for harness "+harnessName)
	}
	if reading.GetParse() == typev1.ParseOutcome_PARSE_OUTCOME_FAILED {
		// No usage content: a trust or named startup screen that won
		// the race after typing blocks the reading; an unchanged
		// screen means the command showed no output, so health goes
		// out alone (03b); anything else is still reported as
		// parse_failed with its redacted raw screen.
		if c := Classify(harnessName, screen); c.Outcome == OutcomeTrust {
			return m.blocked(ctx, a, pane, now, typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED,
				typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN, "trust",
				"folder trust screen: trust the probe folder in your own terminal; nothing typed")
		} else if c.Outcome == OutcomeStartup {
			return m.blocked(ctx, a, pane, now, typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN,
				typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN, c.Kind,
				kindMessage(c.Kind))
		} else if screen == before {
			return m.failed(ctx, a, pane, now, true, typev1.ReasonCode_REASON_CODE_PROBE_COMMAND_TIMEOUT, true,
				"the status command showed no output")
		}
	}
	return m.succeeded(ctx, a, pane, now, reading, "probed "+command)
}

// ensureSession brings the probe session up, respawning a dead one: 03b
// gives the respawn three attempts before PROBE_START_FAILED. A backend
// error that is not a missing session ends the attempt without destroying
// anything: killing on a transient read error could end a live session.
func (m *Manager) ensureSession(ctx context.Context, name, binary string) error {
	for i := 0; i < m.ensureTries; i++ {
		has, err := m.be.HasSession(ctx, name)
		if err != nil {
			return err
		}
		if has {
			dead, err := m.be.PaneDead(ctx, name)
			if err != nil {
				return err
			}
			if !dead {
				return nil
			}
			_ = m.be.KillSession(ctx, name)
		}
		if err := m.be.NewSession(ctx, name, m.dir, binary); err == nil {
			return nil
		}
		if i+1 < m.ensureTries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(m.ensureEvery):
			}
		}
	}
	return errors.New("probe: session would not start")
}

// settle captures until two consecutive screens match: only a stable
// screen is classified, and only a stable answer is parsed. A screen
// that keeps changing through every poll is unsettled, never the last
// render: classifying a repaint could type into a prompt.
func (m *Manager) settle(ctx context.Context, name string) (string, bool) {
	prev, err := m.be.Capture(ctx, name)
	if err != nil {
		return "", false
	}
	for i := 1; i < m.settleN; i++ {
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(m.settleEvery):
		}
		cur, err := m.be.Capture(ctx, name)
		if err != nil {
			return "", false
		}
		if cur == prev {
			return cur, true
		}
		prev = cur
	}
	return "", false
}

// paneKey reads the probe pane's key for events. A key that will not
// read leaves the events without one: health still reports the probe.
func (m *Manager) paneKey(ctx context.Context, name string) *typev1.PaneKey {
	start, err1 := m.be.ServerStartID(ctx)
	pane, err2 := m.be.FirstPane(ctx, name)
	if err1 != nil || err2 != nil || start == "" || pane == "" {
		return nil
	}
	return &typev1.PaneKey{Server: probeServer, ServerStartId: start, PaneId: pane}
}
