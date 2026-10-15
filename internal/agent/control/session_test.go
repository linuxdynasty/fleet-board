package control

import (
	"errors"
	"strings"
	"testing"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/exec"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux/tmuxtest"
	"github.com/linuxdynasty/fleet-board/internal/proc"
)

// testOptions builds Options with a dummy socket no server listens on:
// refusal tests fail before any child runs.
func testOptions() Options {
	caller, err := proc.For("tmux")
	if err != nil {
		panic(err)
	}
	runner, err := proc.NewRunner(nil)
	if err != nil {
		panic(err)
	}
	return Options{
		Runner: runner,
		Caller: caller,
		// A path no server listens on: refusal tests never reach tmux.
		Socket: "/tmp/fleet-control-test-unused/s",
		Server: "default",
		Kind:   typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT,
		Roots:  func() []string { return []string{"/tmp"} },
		ResolveWorkdir: func(w *typev1.Workdir) (string, error) {
			return "/tmp", nil
		},
		Sender:     &stubSender{},
		CheckInput: stubCheckInput,
	}
}

func TestNewRefusesAnIncompleteSetup(t *testing.T) {
	t.Parallel()
	full := testOptions()
	for _, tc := range []struct {
		name   string
		change func(*Options)
	}{
		{"no runner", func(o *Options) { o.Runner = nil }},
		{"no caller", func(o *Options) { o.Caller = proc.Caller{} }},
		{"no socket", func(o *Options) { o.Socket = "" }},
		{"no server name", func(o *Options) { o.Server = "" }},
		{"no kind", func(o *Options) { o.Kind = typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED }},
		{"no workdir resolver", func(o *Options) { o.ResolveWorkdir = nil }},
		{"no sender", func(o *Options) { o.Sender = nil }},
		{"no input check", func(o *Options) { o.CheckInput = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			o := full
			tc.change(&o)
			if _, err := New(o); err == nil {
				t.Fatal("New accepted an incomplete Options")
			}
		})
	}
}

// TestHandlerContract pins the executor seam for F1: the kind the handler
// runs, the capability it advertises for Command.requires, and the restart
// class (EnsureSession is declarative, so a resume re-runs it from the
// start). F1 appears here as a whole word for the capability report.
func TestHandlerContract(t *testing.T) {
	t.Parallel()
	e, err := New(testOptions())
	if err != nil {
		t.Fatal(err)
	}
	h := e.Handler()
	if h.Kind() != "EnsureSession" {
		t.Fatalf("Kind = %q, want EnsureSession (F1)", h.Kind())
	}
	caps := h.Capabilities()
	// 02e "Command kinds and capabilities": tmux.control plus the
	// server-specific one. F1 appears here as a whole word for the
	// capability report.
	want := map[string]uint32{"tmux.control": 1, "tmux.default_server.control": 1}
	if len(caps) != len(want) {
		t.Fatalf("Capabilities = %v, want %v for F1", caps, want)
	}
	for _, c := range caps {
		if want[c.GetName()] != c.GetVersion() || c.GetVersion() != 1 {
			t.Fatalf("Capabilities = %v, want %v for F1", caps, want)
		}
	}
	cmd := &agentv1.Command{Kind: &agentv1.Command_EnsureSession{EnsureSession: &agentv1.EnsureSession{}}}
	if got := h.Class(cmd); got != exec.RepeatSafe {
		t.Fatalf("Class = %v, want RepeatSafe for F1", got)
	}
}

// liveOptions builds Options against a throwaway tmux server with the
// machine's tmux binary: every behavior below runs against real tmux.
func liveOptions(t *testing.T, b tmuxtest.Binary, roots []string) (Options, *tmuxtest.Server) {
	t.Helper()
	srv := tmuxtest.Start(t, b, "-s", "control-base", "exec sleep 60")
	o := testOptions()
	o.Socket = srv.Socket
	if b.Path != "" {
		o.Tmux = b.Path
	}
	o.Roots = func() []string { return roots }
	return o, srv
}

func ensureCmd(name string) *agentv1.EnsureSession {
	return &agentv1.EnsureSession{
		Name:   name,
		Server: typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT,
	}
}

func TestEnsureRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*agentv1.EnsureSession)
		status typev1.Status
		reason typev1.ReasonCode
	}{
		{"empty name", func(c *agentv1.EnsureSession) { c.Name = "" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME},
		{"uppercase name", func(c *agentv1.EnsureSession) { c.Name = "Fleet-X" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME},
		{"spaces in name", func(c *agentv1.EnsureSession) { c.Name = "my session" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME},
		{"harness waits for its slice", func(c *agentv1.EnsureSession) {
			c.Harness = &typev1.Harness{Name: "claude"}
		}, typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED},
		{"resume waits for its slice", func(c *agentv1.EnsureSession) { c.Resume = true }, typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED},
		{"profile without a harness", func(c *agentv1.EnsureSession) { c.Profile = "work" }, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"wrong server kind", func(c *agentv1.EnsureSession) {
			c.Server = typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
		{"shapeless session id", func(c *agentv1.EnsureSession) {
			c.SessionId = &typev1.SessionId{Value: "NOT AN ID"}
		}, typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, err := New(testOptions())
			if err != nil {
				t.Fatal(err)
			}
			cmd := ensureCmd("fleet-ok")
			tc.change(cmd)
			res := e.EnsureSession(t.Context(), cmd)
			if res.GetStatus() != tc.status || res.GetReason() != tc.reason {
				t.Fatalf("status/reason = %v/%v, want %v/%v (%s)",
					res.GetStatus(), res.GetReason(), tc.status, tc.reason, res.GetMessage())
			}
			if res.GetDetail() != nil {
				t.Fatalf("refusal carries a detail: %v", res.GetDetail())
			}
		})
	}
}

// TestEnsureCreatesAndReuses runs F1 against real tmux: a new name creates a
// session tagged for the fleet, the same name with reuse attaches to it, and
// without reuse it is refused. The pane key's server start id is stable, and
// the 46802/1791947485 probe vector pins the base-36 derivation both sides
// share with the control-mode handshake.
func TestEnsureCreatesAndReuses(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			o, srv := liveOptions(t, b, []string{"/tmp"})
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()

			create := ensureCmd("fleet-one")
			create.SessionId = &typev1.SessionId{Value: "sess-1"}
			create.OwnerPrincipal = &typev1.Principal{Id: &typev1.PrincipalId{Value: "owner-1"}}
			create.GoalId = &typev1.GoalId{Value: "goal-1"}
			res := e.EnsureSession(ctx, create)
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("create status/reason = %v/%v (%s)", res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			got := res.GetEnsureSession()
			if got == nil || !got.GetCreated() {
				t.Fatalf("create detail = %v, want created=true", got)
			}
			if got.GetSession().GetTmuxId() == "" || !got.GetSession().GetStartedByFleet() {
				t.Fatalf("session = %v, want a fleet tmux id", got.GetSession())
			}
			key := got.GetPane()
			if key.GetServer() != "default" || key.GetPaneId() == "" || key.GetServerStartId() == "" {
				t.Fatalf("pane key = %v, want server/default, a pane id and a start id", key)
			}
			// The tags the observer reads: environment on the session and
			// the @fleet_* session options, including the first pane's
			// key and the goal id.
			paneKey := "default/" + key.GetServerStartId() + "/" + key.GetPaneId()
			for _, tc := range []struct {
				args []string
				want string
			}{
				{[]string{"show-environment", "-t", "fleet-one", "FLEET_SESSION_ID"}, "FLEET_SESSION_ID=sess-1\n"},
				{[]string{"show-environment", "-t", "fleet-one", "FLEET_PANE_KEY"}, "FLEET_PANE_KEY=" + paneKey + "\n"},
				{[]string{"show-options", "-v", "-t", "fleet-one", "@fleet_session_id"}, "sess-1\n"},
				{[]string{"show-options", "-v", "-t", "fleet-one", "@fleet_owner"}, "owner-1\n"},
				{[]string{"show-options", "-v", "-t", "fleet-one", "@fleet_pane_key"}, paneKey + "\n"},
				{[]string{"show-options", "-v", "-t", "fleet-one", "@fleet_goal_id"}, "goal-1\n"},
			} {
				if got := srv.Run(t, tc.args...); got != tc.want {
					t.Fatalf("%v = %q, want %q", tc.args, got, tc.want)
				}
			}
			if got.GetSession().GetSessionId().GetValue() != "sess-1" {
				t.Fatalf("reported session id = %v, want the stored tag sess-1", got.GetSession().GetSessionId())
			}
			if !got.GetSession().GetStartedByFleet() {
				t.Fatal("created tagged session reports started_by_fleet=false")
			}
			listed := srv.Run(t, "list-sessions", "-F", "#{session_name}")
			if !strings.Contains(listed, "fleet-one") {
				t.Fatalf("list-sessions = %q, want fleet-one", listed)
			}

			again := e.EnsureSession(ctx, ensureCmd("fleet-one"))
			if again.GetStatus() != typev1.Status_STATUS_FAILED || again.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
				t.Fatalf("exists without reuse = %v/%v, want FAILED/INVALID_ARGUMENT",
					again.GetStatus(), again.GetReason())
			}

			reuse := ensureCmd("fleet-one")
			reuse.Reuse = true
			reused := e.EnsureSession(ctx, reuse)
			if reused.GetStatus() != typev1.Status_STATUS_OK || reused.GetEnsureSession().GetCreated() {
				t.Fatalf("reuse = %v, want OK with created=false", reused)
			}
			if reused.GetEnsureSession().GetSession().GetTmuxId() != got.GetSession().GetTmuxId() {
				t.Fatalf("reuse tmux id = %v, want %v",
					reused.GetEnsureSession().GetSession().GetTmuxId(), got.GetSession().GetTmuxId())
			}
			if reused.GetEnsureSession().GetPane().GetServerStartId() != key.GetServerStartId() {
				t.Fatalf("reuse start id changed across calls")
			}
		})
	}
}

// TestEnsureTraversalRefused pins the 01e rule: a ".." element in the work
// path is refused, never clamped into the checkout. Roots are "/" here so
// only the traversal rule can refuse: the clamped path would be allowed on
// every machine (on macOS an unresolved symlink can otherwise refuse first
// and hide a missing check).
func TestEnsureTraversalRefused(t *testing.T) {
	t.Parallel()
	for _, rel := range []string{"../secret", "sub/../../secret", ".."} {
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			o := testOptions()
			o.Roots = func() []string { return []string{"/"} }
			o.ResolveWorkdir = func(w *typev1.Workdir) (string, error) { return "/tmp/fleet-control-repo", nil }
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			cmd := ensureCmd("fleet-trav")
			cmd.Workdir = &typev1.Workdir{RelativePath: rel}
			res := e.EnsureSession(t.Context(), cmd)
			if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY || res.GetReason() != typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED {
				t.Fatalf("status/reason = %v/%v (%s), want REJECTED_BY_POLICY/REPO_ROOT_NOT_ALLOWED",
					res.GetStatus(), res.GetReason(), res.GetMessage())
			}
		})
	}
}

// TestEnsureReuseReportsStoredTags attaches to a session the fleet did not
// create and reports what its tags say: no fleet id, not fleet-owned. The
// result never claims ownership the tags do not show.
func TestEnsureReuseReportsStoredTags(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			o, _ := liveOptions(t, b, []string{"/tmp"})
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			reuse := ensureCmd("control-base")
			reuse.Reuse = true
			res := e.EnsureSession(t.Context(), reuse)
			if res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("reuse status/reason = %v/%v (%s)", res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			got := res.GetEnsureSession()
			if got.GetCreated() {
				t.Fatal("reuse reported created=true")
			}
			if got.GetSession().GetStartedByFleet() {
				t.Fatal("reused untagged session reports started_by_fleet=true")
			}
			if got.GetSession().GetSessionId() != nil {
				t.Fatalf("reused untagged session reports session id %v", got.GetSession().GetSessionId())
			}
		})
	}
}

// TestEnsureRetryAfterCreate is the crash window: the first call created the
// session but its result never reached the log, so the executor re-runs the
// RepeatSafe handler with reuse=false. The stored session tag matches the
// retried session id, so the retry completes OK instead of failing on a
// session that is already the command's own effect.
func TestEnsureRetryAfterCreate(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			o, _ := liveOptions(t, b, []string{"/tmp"})
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			first := ensureCmd("fleet-retry")
			first.SessionId = &typev1.SessionId{Value: "sess-retry"}
			if res := e.EnsureSession(ctx, first); res.GetStatus() != typev1.Status_STATUS_OK {
				t.Fatalf("create status/reason = %v/%v (%s)", res.GetStatus(), res.GetReason(), res.GetMessage())
			}
			retry := ensureCmd("fleet-retry")
			retry.SessionId = &typev1.SessionId{Value: "sess-retry"}
			res := e.EnsureSession(ctx, retry)
			if res.GetStatus() != typev1.Status_STATUS_OK || res.GetEnsureSession().GetCreated() {
				t.Fatalf("retry status/created = %v/%v, want OK with created=false",
					res.GetStatus(), res.GetEnsureSession().GetCreated())
			}

			// Another session id on the same name is a real conflict.
			clash := ensureCmd("fleet-retry")
			clash.SessionId = &typev1.SessionId{Value: "sess-other"}
			res = e.EnsureSession(ctx, clash)
			if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT {
				t.Fatalf("clash status/reason = %v/%v, want FAILED/INVALID_ARGUMENT",
					res.GetStatus(), res.GetReason())
			}
		})
	}
}

// TestEnsureStartsAMissingServer: with the start allowed, the first ensure
// on a dead socket creates the session (new-session starts the server).
// Without it, the same call reads TMUX_SERVER_DOWN.
func TestEnsureStartsAMissingServer(t *testing.T) {
	t.Parallel()
	for _, b := range tmuxtest.Binaries() {
		t.Run(b.Name, func(t *testing.T) {
			t.Parallel()
			o, srv := liveOptions(t, b, []string{"/tmp"})
			srv.Kill(t)
			o.StartServer = func() bool { return true }
			e, err := New(o)
			if err != nil {
				t.Fatal(err)
			}
			res := e.EnsureSession(t.Context(), ensureCmd("fleet-fresh"))
			if res.GetStatus() != typev1.Status_STATUS_OK || !res.GetEnsureSession().GetCreated() {
				t.Fatalf("start status/created = %v/%v (%s), want OK created=true",
					res.GetStatus(), res.GetEnsureSession().GetCreated(), res.GetMessage())
			}
		})
	}
}

// TestEnsureStartIDVector pins the pid/start-time derivation against a value
// worked out by hand (pid 46802, start 1791947485 -> "1042-tmvnj1"), the same
// rule the control-mode handshake uses, so a PaneKey from F1 matches the
// observer's on the same server.
func TestEnsureStartIDVector(t *testing.T) {
	t.Parallel()
	if got := startID(46802, 1791947485); got != "1042-tmvnj1" {
		t.Fatalf("startID = %q, want 1042-tmvnj1", got)
	}
}

func TestEnsureDrainingAndRoots(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("draining refuses a harness start", func(t *testing.T) {
		t.Parallel()
		o := testOptions()
		o.Draining = func() bool { return true }
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		cmd := ensureCmd("fleet-two")
		cmd.Harness = &typev1.Harness{Name: "claude"}
		res := e.EnsureSession(ctx, cmd)
		if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY || res.GetReason() != typev1.ReasonCode_REASON_CODE_DRAINING {
			t.Fatalf("status/reason = %v/%v, want REJECTED_BY_POLICY/DRAINING",
				res.GetStatus(), res.GetReason())
		}
	})

	t.Run("draining lets a login shell through", func(t *testing.T) {
		t.Parallel()
		o := testOptions()
		o.Draining = func() bool { return true }
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		// The dummy socket answers nothing: the call runs past the drain
		// gate and fails at tmux, proving a login shell is not a harness
		// start (01f M4).
		res := e.EnsureSession(ctx, ensureCmd("fleet-three"))
		if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_TMUX_SERVER_DOWN {
			t.Fatalf("status/reason = %v/%v, want FAILED/TMUX_SERVER_DOWN past the drain gate",
				res.GetStatus(), res.GetReason())
		}
	})

	t.Run("no roots configured refuses", func(t *testing.T) {
		t.Parallel()
		o := testOptions()
		o.Roots = func() []string { return nil }
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		res := e.EnsureSession(ctx, ensureCmd("fleet-four"))
		if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY || res.GetReason() != typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED {
			t.Fatalf("status/reason = %v/%v, want REJECTED_BY_POLICY/REPO_ROOT_NOT_ALLOWED",
				res.GetStatus(), res.GetReason())
		}
	})

	t.Run("workdir outside the roots refuses", func(t *testing.T) {
		t.Parallel()
		o := testOptions()
		o.Roots = func() []string { return []string{"/srv/repos"} }
		o.ResolveWorkdir = func(w *typev1.Workdir) (string, error) { return "/tmp/elsewhere", nil }
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		res := e.EnsureSession(ctx, ensureCmd("fleet-five"))
		if res.GetStatus() != typev1.Status_STATUS_REJECTED_BY_POLICY || res.GetReason() != typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED {
			t.Fatalf("status/reason = %v/%v, want REJECTED_BY_POLICY/REPO_ROOT_NOT_ALLOWED",
				res.GetStatus(), res.GetReason())
		}
	})

	t.Run("unresolvable workdir fails", func(t *testing.T) {
		t.Parallel()
		o := testOptions()
		o.ResolveWorkdir = func(w *typev1.Workdir) (string, error) { return "", errors.New("no checkout for repo") }
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		res := e.EnsureSession(ctx, ensureCmd("fleet-six"))
		if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING {
			t.Fatalf("status/reason = %v/%v, want FAILED/CHECKOUT_MISSING",
				res.GetStatus(), res.GetReason())
		}
	})

	t.Run("no server fails", func(t *testing.T) {
		t.Parallel()
		o := testOptions()
		o.Socket = "/tmp/fleet-control-test-no-server/s"
		e, err := New(o)
		if err != nil {
			t.Fatal(err)
		}
		res := e.EnsureSession(ctx, ensureCmd("fleet-seven"))
		if res.GetStatus() != typev1.Status_STATUS_FAILED || res.GetReason() != typev1.ReasonCode_REASON_CODE_TMUX_SERVER_DOWN {
			t.Fatalf("status/reason = %v/%v (%s), want FAILED/TMUX_SERVER_DOWN",
				res.GetStatus(), res.GetReason(), res.GetMessage())
		}
	})
}
