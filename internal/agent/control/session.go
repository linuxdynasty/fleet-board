package control

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/proc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Runner runs one tmux CLI call. *proc.Runner satisfies it; the seam keeps
// tests on the production path (a throwaway server) instead of a fake.
type Runner interface {
	Run(ctx context.Context, sp proc.Spec) (proc.Result, error)
}

// Options builds an Ensurer bound to one tmux server. The wiring row maps a
// TmuxServerKind to its socket and constructs one Ensurer per server.
type Options struct {
	Runner Runner
	// Caller is proc.For("tmux"): the one-shot caps for tmux CLI calls.
	Caller proc.Caller
	// Tmux is the tmux binary; empty means "tmux" on PATH.
	Tmux string
	// Socket is the server's socket path, passed as -S. Always explicit:
	// there is no default server.
	Socket string
	// Server is the PaneKey server name ("default", "fleet", "fleet-probe").
	Server string
	// Kind is the TmuxServerKind this Ensurer serves; a command for
	// another kind is refused, so a session never lands on the wrong
	// server (the probe server stays probes-only).
	Kind typev1.TmuxServerKind
	// Roots are the local policy's allowed repo roots. Empty means none
	// are configured, and every ensure is refused: the policy is the
	// ceiling, and a session starts nowhere unlisted.
	Roots func() []string
	// Draining reports the drain state (plan row 24 owns it). Nil means
	// never draining.
	Draining func() bool
	// StartServer allows creating the tmux server when the socket has
	// none: new-session starts it implicitly (03d failure table). Nil
	// means never: a missing server reads TMUX_SERVER_DOWN. The wiring
	// row allows it from the local default_server policy.
	StartServer func() bool
	// ResolveWorkdir maps the command's Workdir (a repo plus a path under
	// it) to a local checkout path. The repo inventory (plan row 9) owns
	// the production resolver; this slice takes the seam.
	ResolveWorkdir func(*typev1.Workdir) (string, error)
	// Now is the clock for result timestamps. Nil means time.Now.
	Now func() time.Time
}

// Ensurer runs EnsureSession (F1) against one tmux server. One goroutine
// owns each Ensurer call; tmux serializes the commands itself.
type Ensurer struct {
	runner  Runner
	caller  proc.Caller
	tmux    string
	socket  string
	server  string
	kind    typev1.TmuxServerKind
	roots   func() []string
	drain   func() bool
	start   func() bool
	workdir func(*typev1.Workdir) (string, error)
	now     func() time.Time
}

// New checks the setup and fails fast: a half-built Ensurer must never
// answer a command.
func New(o Options) (*Ensurer, error) {
	if o.Runner == nil {
		return nil, errors.New("control: Runner is required")
	}
	if o.Caller.Name() == "" {
		return nil, errors.New("control: Caller is required (proc.For(\"tmux\"))")
	}
	if o.Socket == "" {
		return nil, errors.New("control: Socket is required (there is no default server)")
	}
	if o.Server == "" {
		return nil, errors.New("control: Server is required")
	}
	if o.Kind == typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED {
		return nil, errors.New("control: Kind is required")
	}
	if o.Roots == nil {
		return nil, errors.New("control: Roots is required")
	}
	if o.ResolveWorkdir == nil {
		return nil, errors.New("control: ResolveWorkdir is required")
	}
	e := &Ensurer{
		runner:  o.Runner,
		caller:  o.Caller,
		tmux:    o.Tmux,
		socket:  o.Socket,
		server:  o.Server,
		kind:    o.Kind,
		roots:   o.Roots,
		workdir: o.ResolveWorkdir,
		now:     o.Now,
		start:   o.StartServer,
	}
	if e.tmux == "" {
		e.tmux = "tmux"
	}
	if e.drain = o.Draining; e.drain == nil {
		e.drain = func() bool { return false }
	}
	if e.start == nil {
		e.start = func() bool { return false }
	}
	if e.now == nil {
		e.now = time.Now
	}
	return e, nil
}

// nameRE is the fleet session name (command_tmux.proto EnsureSession.name),
// re-checked on the agent: the hub validates, the agent fails closed.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// idRE is 02e "Id format", shared with the observer's tag shapes: a value
// written into the session environment must keep its shape or the observer
// drops it whole.
var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// principalRE is the observer's shape for FLEET_OWNER.
var principalRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,127}$`)

// refuse builds a refusal: nothing was done, or (FAILED) the attempt failed.
func refuse(st typev1.Status, why typev1.ReasonCode, msg string) *agentv1.CommandResult {
	return &agentv1.CommandResult{Status: st, Reason: why, Message: msg}
}

// EnsureSession creates the named session or, with reuse, attaches to the
// one already there. This slice starts a login shell only: a harness to
// start, or resume of a harness conversation, is refused until the adapter
// start surface lands in a later slice of row 18.
func (e *Ensurer) EnsureSession(ctx context.Context, cmd *agentv1.EnsureSession) *agentv1.CommandResult {
	name := cmd.GetName()
	if !nameRE.MatchString(name) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_NAME,
			"session name must match [a-z0-9][a-z0-9-]{0,31}")
	}
	if k := cmd.GetServer(); k != typev1.TmuxServerKind_TMUX_SERVER_KIND_UNSPECIFIED && k != e.kind {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"this ensurer does not serve that tmux server")
	}
	harness := cmd.GetHarness().GetName()
	starting := harness != "" || cmd.GetResume()
	if starting && e.drain() {
		// 01f M4: while draining, harness-starting F1 is refused. A
		// login shell is not a harness start and runs on.
		return refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_DRAINING,
			"the machine is draining and starts no harness sessions")
	}
	if starting {
		return refuse(typev1.Status_STATUS_UNSUPPORTED, typev1.ReasonCode_REASON_CODE_HARNESS_NOT_ALLOWED,
			"this agent build starts login shells only; harness start lands in a later slice")
	}
	if cmd.GetProfile() != "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"a harness profile without a harness means nothing")
	}
	sessionID := cmd.GetSessionId().GetValue()
	if sessionID != "" && !idRE.MatchString(sessionID) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"session id must match the 02e id format")
	}
	owner := cmd.GetOwnerPrincipal()
	ownerID := owner.GetId().GetValue()
	if ownerID != "" && !principalRE.MatchString(ownerID) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"owner id must match the principal shape")
	}
	goalID := cmd.GetGoalId().GetValue()
	if goalID != "" && !idRE.MatchString(goalID) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"goal id must match the 02e id format")
	}
	dir, err := e.workdir(cmd.GetWorkdir())
	if err != nil || dir == "" {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_CHECKOUT_MISSING,
			"the workdir resolves to no local checkout")
	}
	if rel := cmd.GetWorkdir().GetRelativePath(); rel != "" {
		// 01e: the agent rejects "..", it never clamps it into the
		// checkout: "../secret" must not quietly become "<repo>/secret".
		// The raw path is checked: cleaning first would resolve the
		// traversal away before it is seen.
		for _, el := range strings.Split(rel, "/") {
			if el == ".." {
				return refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
					"the work path climbs out of its checkout")
			}
		}
		dir = filepath.Join(dir, filepath.Clean("/"+rel))
	}
	if !e.underRoots(dir) {
		return refuse(typev1.Status_STATUS_REJECTED_BY_POLICY, typev1.ReasonCode_REASON_CODE_REPO_ROOT_NOT_ALLOWED,
			"the workdir is under no allowed repo root")
	}

	found, err := e.hasSession(ctx, name)
	if err != nil {
		if !isDown(err.Error()) || !e.start() {
			return e.down(err)
		}
		// The server is missing and starting it is allowed:
		// new-session below starts it implicitly.
	} else if found {
		return e.existing(ctx, name, cmd.GetReuse(), sessionID)
	}
	if err := e.create(ctx, createArgs{
		name: name, dir: dir, window: cmd.GetWindowName(),
		sessionID: sessionID, owner: ownerID, goalID: goalID,
	}); err != nil {
		if isDuplicate(err) {
			// Lost the race with another creator: the session is
			// there now, so take the exists path.
			if found, derr := e.hasSession(ctx, name); derr != nil {
				return e.down(derr)
			} else if found {
				return e.existing(ctx, name, cmd.GetReuse(), sessionID)
			}
		}
		return e.down(err)
	}
	return e.describe(ctx, name, true)
}

// existing answers for a session that is already there. With reuse it
// attaches; without, only a stored session tag matching the requested
// session id completes the call: that is this command's own effect seen
// again after its result was lost (F1 is idempotent, 01f), while any other
// session under the name stays a conflict.
func (e *Ensurer) existing(ctx context.Context, name string, reuse bool, sessionID string) *agentv1.CommandResult {
	if !reuse {
		if sessionID == "" {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"session "+name+" exists and reuse is false")
		}
		stored, err := e.sessionTag(ctx, name, "@fleet_session_id")
		if err != nil {
			return e.down(err)
		}
		if stored != sessionID {
			return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"session "+name+" exists and reuse is false")
		}
	}
	return e.describe(ctx, name, false)
}

// underRoots reports whether dir sits under a configured root. Both sides
// are symlink-resolved first (/tmp is a link on macOS); no roots configured
// means nowhere is allowed.
func (e *Ensurer) underRoots(dir string) bool {
	roots := e.roots()
	if len(roots) == 0 {
		return false
	}
	clean, err := filepath.EvalSymlinks(dir)
	if err != nil {
		clean = filepath.Clean(dir)
	}
	for _, r := range roots {
		base, err := filepath.EvalSymlinks(r)
		if err != nil {
			base = filepath.Clean(r)
		}
		// A root of "/" matches everything: appending another
		// separator would look for "//".
		edge := strings.TrimSuffix(base, string(filepath.Separator)) + string(filepath.Separator)
		if clean == base || strings.HasPrefix(clean, edge) {
			return true
		}
	}
	return false
}

// down maps a tmux call failure: a dead or missing server reads
// TMUX_SERVER_DOWN, anything else is INTERNAL.
func (e *Ensurer) down(err error) *agentv1.CommandResult {
	msg := err.Error()
	if isDown(msg) {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_TMUX_SERVER_DOWN,
			"the tmux server answers nothing")
	}
	return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_INTERNAL,
		"the tmux call failed")
}

// isDown matches the connection failures tmux prints across versions: "no
// server running on ...", "error connecting to ...", "failed to connect".
func isDown(msg string) bool {
	for _, s := range []string{"no server running", "error connecting", "failed to connect", "no such file or directory"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// isDuplicate matches the lost create race: "duplicate session: ...".
func isDuplicate(err error) bool {
	return strings.Contains(err.Error(), "duplicate session")
}

// describe builds the OK result for the named session: its tmux id, the
// first pane's key, and whether this call created it. Ownership and ids
// come from the session's stored tags, never from the request: attaching
// to a developer session reports it as the developer's, and a session
// with no panes left reads SESSION_NOT_FOUND.
func (e *Ensurer) describe(ctx context.Context, name string, created bool) *agentv1.CommandResult {
	out, err := e.tmuxCall(ctx, "list-panes", "-t", name, "-F", "#{session_id} #{pane_id}")
	if err != nil {
		return e.down(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	f := strings.Fields(lines[0])
	if len(f) < 2 {
		return refuse(typev1.Status_STATUS_FAILED, typev1.ReasonCode_REASON_CODE_SESSION_NOT_FOUND,
			"session "+name+" went away")
	}
	start, err := e.startID(ctx)
	if err != nil {
		return e.down(err)
	}
	key := e.server + "/" + start + "/" + f[1]
	sessionID, err := e.sessionTag(ctx, name, "@fleet_session_id")
	if err != nil {
		return e.down(err)
	}
	ownerID, err := e.sessionTag(ctx, name, "@fleet_owner")
	if err != nil {
		return e.down(err)
	}
	paneKey, err := e.sessionTag(ctx, name, "@fleet_pane_key")
	if err != nil {
		return e.down(err)
	}
	session := &agentv1.TmuxSession{
		TmuxId:          f[0],
		Name:            cap256(name),
		StartedByFleet:  paneKey == key,
		CreateTime:      timestamppb.New(e.now()),
		AttachedClients: 0,
	}
	// The observer reports a tag only when it has its id's shape; the
	// result matches that view, so the two never disagree.
	if idRE.MatchString(sessionID) {
		session.SessionId = &typev1.SessionId{Value: sessionID}
	}
	if principalRE.MatchString(ownerID) {
		session.OwnerPrincipal = &typev1.Principal{Id: &typev1.PrincipalId{Value: ownerID}}
	}
	verb := "reused session "
	if created {
		verb = "created session "
	}
	return &agentv1.CommandResult{
		Status:  typev1.Status_STATUS_OK,
		Reason:  typev1.ReasonCode_REASON_CODE_UNSPECIFIED,
		Message: verb + name,
		Detail: &agentv1.CommandResult_EnsureSession{EnsureSession: &agentv1.EnsureSessionResult{
			Session: session,
			Created: created,
			Pane: &typev1.PaneKey{
				Server:        e.server,
				ServerStartId: start,
				PaneId:        f[1],
			},
		}},
	}
}
