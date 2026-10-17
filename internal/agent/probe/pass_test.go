package probe

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
)

// runManager starts a manager on be/em and returns it with a cancel that
// stops it. Handlers block until the manager answers, so every Run below
// exercises the production path: handler -> manager goroutine -> backend.
func runManager(t *testing.T, be *fakeBackend, em *fakeEmitter) *Manager {
	t.Helper()
	m, err := NewManager(testOptions(be, em))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("manager did not stop")
		}
	})
	return m
}

func runEnsure(t *testing.T, m *Manager, cmd *agentv1.Command) *agentv1.CommandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return m.EnsureProbeHandler().Run(ctx, exec.Request{Command: cmd})
}

func runStop(t *testing.T, m *Manager, cmd *agentv1.Command) *agentv1.CommandResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return m.StopProbeHandler().Run(ctx, exec.Request{Command: cmd})
}

// TestPassTrustTypesNothing is the A4 case: a probe pane on a trust screen
// reports FOLDER_UNTRUSTED, sends health only, and presses no key.
func TestPassTrustTypesNothing(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	trust := loadFixture(t, "../../harness/codex/testdata/0.160.0/trust-folder-git.txt")
	be.captures = []string{trust, trust}
	m := runManager(t, be, em)

	res := runEnsure(t, m, ensureCommand("probe-1", "acc-1", "key-1", "codex", true))
	if res.GetStatus() != typev1.Status_STATUS_FAILED {
		t.Fatalf("status = %v, want FAILED", res.GetStatus())
	}
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED {
		t.Errorf("reason = %v, want FOLDER_UNTRUSTED", res.GetReason())
	}
	if res.GetMetadata()["kind"] != "trust" {
		t.Errorf("metadata kind = %q, want trust", res.GetMetadata()["kind"])
	}
	if len(be.sent) != 0 {
		t.Errorf("typed %v on a trust screen; a trust screen takes no key", be.sent)
	}
	health := em.health()
	if len(health) != 1 {
		t.Fatalf("health events = %d, want 1", len(health))
	}
	h := health[0]
	if h.GetAlive() {
		t.Errorf("alive = true on a trust screen")
	}
	// The result names FOLDER_UNTRUSTED for the owner (01 C3) while
	// health carries STARTUP_PROMPT_SEEN for pacing (03b).
	if h.GetLastFailure() != typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN {
		t.Errorf("last_failure = %v, want STARTUP_PROMPT_SEEN", h.GetLastFailure())
	}
	if h.GetConsecutiveFailures() != 1 {
		t.Errorf("consecutive_failures = %d, want 1", h.GetConsecutiveFailures())
	}
	if len(em.readings()) != 0 {
		t.Errorf("usage readings = %d on a trust screen, want none", len(em.readings()))
	}
	// The pane key is read after the session is ensured, so even the
	// first pass names the live pane.
	if got := h.GetPane(); got == nil || got.GetPaneId() != "%0" || got.GetServer() != "fleet-probe" {
		t.Errorf("health pane = %+v, want the ensured session's first pane", got)
	}
}

// TestPassUpdateTypesNothing is the A3 case: the Codex update screen ends
// the pass with STARTUP_PROMPT_SEEN and no key press. A blind Enter here
// once ran a package upgrade (01b); the pass never risks it.
func TestPassUpdateTypesNothing(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	update := loadFixture(t, "../../harness/codex/testdata/0.160.0/startup-update-prompt.txt")
	be.captures = []string{update, update}
	m := runManager(t, be, em)

	res := runEnsure(t, m, ensureCommand("probe-1", "acc-1", "key-1", "codex", true))
	if res.GetStatus() != typev1.Status_STATUS_FAILED {
		t.Fatalf("status = %v, want FAILED", res.GetStatus())
	}
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN {
		t.Errorf("reason = %v, want STARTUP_PROMPT_SEEN", res.GetReason())
	}
	if res.GetMetadata()["kind"] != "update" {
		t.Errorf("metadata kind = %q, want update", res.GetMetadata()["kind"])
	}
	if len(be.sent) != 0 {
		t.Errorf("typed %v on an update screen; a startup screen takes no key", be.sent)
	}
	if len(em.readings()) != 0 {
		t.Errorf("usage readings = %d on an update screen, want none", len(em.readings()))
	}
	health := em.health()
	if len(health) != 1 || health[0].GetAlive() {
		t.Fatalf("health = %+v, want one event with alive=false", health)
	}
	if health[0].GetLastFailure() != typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN {
		t.Errorf("last_failure = %v, want STARTUP_PROMPT_SEEN", health[0].GetLastFailure())
	}
}

// TestPassDaemonSettingsTypesNothing is the unkinded startup case: the
// Codex daemon-settings screen blocks on a choice the fleet never answers,
// so the pass ends with STARTUP_PROMPT_SEEN, alive=false, health only and
// no key press. The kind stays empty rather than guessed.
func TestPassDaemonSettingsTypesNothing(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	daemon := loadFixture(t, "../../harness/codex/testdata/0.160.0/startup-daemon-settings-incompatible.txt")
	be.captures = []string{daemon, daemon}
	m := runManager(t, be, em)

	res := runEnsure(t, m, ensureCommand("probe-1", "acc-1", "key-1", "codex", true))
	if res.GetStatus() != typev1.Status_STATUS_FAILED {
		t.Fatalf("status = %v, want FAILED", res.GetStatus())
	}
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN {
		t.Errorf("reason = %v, want STARTUP_PROMPT_SEEN", res.GetReason())
	}
	if kind := res.GetMetadata()["kind"]; kind != "" {
		t.Errorf("metadata kind = %q, want empty (no guessed kind)", kind)
	}
	if len(be.sent) != 0 {
		t.Errorf("typed %v on a daemon-settings screen; a startup screen takes no key", be.sent)
	}
	if len(em.readings()) != 0 {
		t.Errorf("usage readings = %d on a daemon-settings screen, want none", len(em.readings()))
	}
	health := em.health()
	if len(health) != 1 || health[0].GetAlive() {
		t.Fatalf("health = %+v, want one event with alive=false", health)
	}
	if health[0].GetLastFailure() != typev1.ReasonCode_REASON_CODE_STARTUP_PROMPT_SEEN {
		t.Errorf("last_failure = %v, want STARTUP_PROMPT_SEEN", health[0].GetLastFailure())
	}
}

// TestPassReadyTypesStatusOnce is the A2 case: on a proven idle screen the
// pass types only the status command, then reports health and a reading.
func TestPassReadyTypesStatusOnce(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	ready := loadFixture(t, "../../harness/codex/testdata/0.160.0/ready.txt")
	be.captures = []string{ready, ready, ready, ready}
	m := runManager(t, be, em)

	res := runEnsure(t, m, ensureCommand("probe-1", "acc-1", "key-1", "codex", true))
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("status = %v (%v), want OK", res.GetStatus(), res.GetReason())
	}
	if len(be.sent) != 2 || be.sent[0] != "literal /status" || be.sent[1] != "key Enter" {
		t.Errorf("sent = %v, want exactly [/status, Enter]", be.sent)
	}
	health := em.health()
	if len(health) != 1 {
		t.Fatalf("health events = %d, want 1", len(health))
	}
	if !health[0].GetAlive() || health[0].GetConsecutiveFailures() != 0 {
		t.Errorf("health = %+v, want alive with no failures", health[0])
	}
	if health[0].GetLastSuccessTime() == nil {
		t.Errorf("health carries no last success time")
	}
	readings := em.readings()
	if len(readings) != 1 {
		t.Fatalf("usage readings = %d, want 1", len(readings))
	}
	r := readings[0]
	if len(r.GetBuckets()) == 0 {
		t.Errorf("reading carries no buckets")
	}
	if r.GetParse() != typev1.ParseOutcome_PARSE_OUTCOME_OK && r.GetParse() != typev1.ParseOutcome_PARSE_OUTCOME_PARTIAL {
		t.Errorf("reading parse = %v, want OK or PARTIAL", r.GetParse())
	}
	if r.GetSource() != typev1.UsageSource_USAGE_SOURCE_PROBE {
		t.Errorf("reading source = %v, want PROBE", r.GetSource())
	}
}

// TestPassClaudeTrust is the trust case on a second harness: the session
// starts in the probe folder with the harness binary and still types nothing.
func TestPassClaudeTrust(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	trust := loadFixture(t, "../../harness/claude/testdata/2.1.296/trust-folder.txt")
	be.captures = []string{trust, trust}
	m := runManager(t, be, em)

	res := runEnsure(t, m, ensureCommand("probe-9", "acc-9", "key-9", "claude", true))
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_FOLDER_UNTRUSTED {
		t.Errorf("reason = %v, want FOLDER_UNTRUSTED", res.GetReason())
	}
	if len(be.sent) != 0 {
		t.Errorf("typed %v on a trust screen", be.sent)
	}
	for name, bin := range be.sessions {
		// The session runs the resolved PATH lookup, not the bare
		// name: the tmux server need not share the agent's PATH.
		if bin != "/bin/claude" {
			t.Errorf("session %s started %q, want the resolved claude path", name, bin)
		}
		if be.dirs[name] != "/probe" {
			t.Errorf("session %s started in %q, want the probe folder", name, be.dirs[name])
		}
	}
}

// TestPassStatusNoOutput is the quiet-command case: the status command
// leaves the idle screen unchanged, so no usage screen was read and only
// health goes out (03b), never a parse-failed reading for a screen that
// simply shows no output.
func TestPassStatusNoOutput(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	ready := loadFixture(t, "../../harness/muse/testdata/1.4.6/startup-ready.txt")
	be.captures = []string{ready, ready, ready, ready}
	m := runManager(t, be, em)

	res := runEnsure(t, m, ensureCommand("probe-1", "acc-1", "key-1", "muse", true))
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_PROBE_COMMAND_TIMEOUT {
		t.Errorf("reason = %v, want PROBE_COMMAND_TIMEOUT", res.GetReason())
	}
	if len(be.sent) != 2 {
		t.Errorf("sent = %v, want the one typed status command", be.sent)
	}
	if len(em.readings()) != 0 {
		t.Errorf("usage readings = %d with no output, want none", len(em.readings()))
	}
	if len(em.health()) != 1 {
		t.Fatalf("health events = %d, want 1", len(em.health()))
	}
}

// TestPassUnsettledScreen never classifies a repaint: a screen that keeps
// changing through every poll ends the pass without typing.
func TestPassUnsettledScreen(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	be.captures = []string{"screen-a", "screen-b"}
	m := runManager(t, be, em)

	res := runEnsure(t, m, ensureCommand("probe-1", "acc-1", "key-1", "codex", true))
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_PROBE_COMMAND_TIMEOUT {
		t.Errorf("reason = %v, want PROBE_COMMAND_TIMEOUT", res.GetReason())
	}
	if len(be.sent) != 0 {
		t.Errorf("typed %v on an unsettled screen", be.sent)
	}
	if len(em.readings()) != 0 || len(em.health()) != 1 {
		t.Errorf("readings = %d, health = %d; want 0 and 1", len(em.readings()), len(em.health()))
	}
}

// TestStopProbeTearsDown stops the session and stays quiet on a repeat.
func TestStopProbeTearsDown(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	m := runManager(t, be, em)

	res := runEnsure(t, m, ensureCommand("probe-1", "acc-1", "key-1", "codex", false))
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("ensure status = %v, want OK", res.GetStatus())
	}
	if len(be.sessions) != 0 || len(be.sent) != 0 {
		t.Errorf("schedule-only ensure created %d sessions and typed %v; want neither", len(be.sessions), be.sent)
	}

	res = runStop(t, m, stopCommand("probe-1"))
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Fatalf("stop status = %v, want OK", res.GetStatus())
	}
	res = runStop(t, m, stopCommand("probe-1"))
	if res.GetStatus() != typev1.Status_STATUS_OK {
		t.Errorf("second stop status = %v, want OK (declarative)", res.GetStatus())
	}
}

// TestEnsureProbeRefusals never touches tmux for commands it cannot run.
func TestEnsureProbeRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		command *agentv1.Command
		reason  typev1.ReasonCode
	}{
		{name: "opencode takes no probe session", command: ensureCommand("p", "a", "k", "opencode", true), reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{name: "unknown harness", command: ensureCommand("p", "a", "k", "cobol", true), reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{name: "empty probe id", command: ensureCommand("", "a", "k", "codex", true), reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{name: "empty account key", command: ensureCommand("p", "a", "", "codex", true), reason: typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			be, em := newFakeBackend(t), &fakeEmitter{}
			m := runManager(t, be, em)
			res := runEnsure(t, m, tc.command)
			if res.GetReason() != tc.reason {
				t.Errorf("reason = %v, want %v", res.GetReason(), tc.reason)
			}
			if len(be.sessions) != 0 || len(be.sent) != 0 {
				t.Errorf("refused command created %d sessions and typed %v", len(be.sessions), be.sent)
			}
			if len(em.events) != 0 {
				t.Errorf("refused command emitted %d events", len(em.events))
			}
		})
	}
}

// TestMissingBinary reports the install failure without a session.
func TestMissingBinary(t *testing.T) {
	t.Parallel()
	be, em := newFakeBackend(t), &fakeEmitter{}
	opts := testOptions(be, em)
	opts.LookPath = func(string) (string, error) { return "", errNotFound }
	m, err := NewManager(opts)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	reqCtx, reqCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer reqCancel()
	res := m.EnsureProbeHandler().Run(reqCtx, exec.Request{Command: ensureCommand("p", "a", "k", "codex", true)})
	if res.GetReason() != typev1.ReasonCode_REASON_CODE_HARNESS_NOT_INSTALLED {
		t.Errorf("reason = %v, want HARNESS_NOT_INSTALLED", res.GetReason())
	}
	if len(be.sessions) != 0 {
		t.Errorf("created %d sessions with no binary", len(be.sessions))
	}
}
