package conn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/hostname"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const (
	scope = "github.com/linuxdynasty/fleet-board/internal/agent/conn"
	// DefaultTimeout bounds one enroll, every try included (defaults.md).
	DefaultTimeout = 60 * time.Second
	// DefaultAttempts is how many times one enroll calls the hub (defaults.md).
	DefaultAttempts = 4
	// maxMessageBytes is the 1 MiB message cap of 02e.
	maxMessageBytes = 1 << 20
	maxHubMessage   = 200
)

var (
	// ErrAlreadyEnrolled means the state folder already holds an identity.
	// Enroll never replaces it. Until re-enroll exists (later in plan row 4,
	// and row 13a's previous_machine), the owner removes identity.json and
	// keeps agent.db, whose install_id survives re-enrollment (agent.proto).
	ErrAlreadyEnrolled = errors.New("conn: this machine is already enrolled")
	// ErrHubUntrusted means the TLS handshake with the hub failed its proof:
	// the pin to the CA in the token, the name, or the key. Nothing was sent
	// (02g A1 step 2).
	ErrHubUntrusted = errors.New("conn: the hub's certificate does not match the CA in the token; nothing was sent")
	// ErrBadResponse means the hub's EnrollResponse failed the agent's checks;
	// nothing was saved and the pending key is kept for a retry.
	ErrBadResponse = errors.New("conn: the hub's enroll response is not valid")
	// ErrHubFailed means the hub could not finish the enroll and nothing was
	// saved. When it stayed busy or out of reach through every try, the
	// error says to run the same command again (the hub replays); when it
	// failed on its side with its ErrorDetail, a rerun fails the same way.
	ErrHubFailed = errors.New("conn: the hub could not finish the enroll")
)

// Retry bounds the tries of one enroll; zero fields take the defaults.
type Retry struct {
	Attempts  int
	FirstWait time.Duration
	MaxWait   time.Duration
}

// RefusedError is the hub's refusal of the request itself: a non-retryable
// ErrorDetail reason (02g), its code and the hub's message.
type RefusedError struct {
	Code    connect.Code
	Reason  typev1.ReasonCode
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("the hub refused to enroll this machine: %s (%s): %s", reasonName(e.Reason), e.Code, e.Message)
}

// Options are the inputs of [Enroll].
type Options struct {
	// Dir is the agent state folder (04 Q9); see [DefaultDir].
	Dir string
	// Hub is the hub address, host or host:port. Empty browses with
	// Discover ([ErrNoHubFound] when no hub holds the token's CA), or is
	// [ErrNoHub] with no Discover.
	Hub string
	// Discover finds the hub when Hub is empty; BrowseTimeout bounds its
	// one browse (zero is [DefaultBrowseTimeout]).
	Discover      Discover
	BrowseTimeout time.Duration
	// Token is the enroll token text the owner pasted.
	Token string
	// Version is the agent version, sent in the facts.
	Version string
	// Facts default to [BasicFacts].
	Facts     *agentv1.MachineFacts
	Telemetry *telemetry.Telemetry
	// Timeout bounds the whole enroll, every try included; 0 means
	// [DefaultTimeout].
	Timeout time.Duration
	Retry   Retry
}

// Result is a finished enrollment.
type Result struct {
	Identity Identity
	// Replayed is true when the hub returned its stored answer to an
	// earlier try with the same token, install_id and key.
	Replayed bool
	// HubAddress is the address enrolled with, now cached; Discovered
	// says mDNS found it.
	HubAddress string
	Discovered bool
	// Hostname is the one this machine reported; HostnameMismatch is true
	// when the hub expected another (compared the hub's way).
	Hostname         string
	HostnameMismatch bool
}

// enrollRun carries one Enroll's logger and span.
type enrollRun struct {
	log  *slog.Logger
	span trace.Span
	// peer is the hub mDNS found, for the logs; empty with --hub.
	peer string
}

// Enroll runs 02g A1 on this machine: check the token and address, make or
// reuse the pending key, call Enroll over TLS pinned to the token's CA
// (no client certificate), check the response, then save the identity and
// the hub address. Nothing reaches the network before the token parses,
// the address is given and the folder is private, and an enrolled folder
// is never replaced.
func Enroll(ctx context.Context, opts Options) (Result, error) {
	if opts.Telemetry == nil {
		return Result{}, errors.New("conn: Enroll needs telemetry")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errEnrollCap)
	defer cancel()
	ctx, span := opts.Telemetry.Tracer(scope).Start(ctx, "agent.enroll")
	defer span.End()
	r := &enrollRun{log: opts.Telemetry.Logger.With("component", "conn"), span: span}
	res, step, err := enroll(ctx, r, opts)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, step)
		hub := opts.Hub
		if r.peer != "" {
			hub = r.peer
		}
		r.failure(ctx, step, err, hub)
		return Result{}, err
	}
	r.log.InfoContext(ctx, "enroll.completed", "machine_id", res.Identity.MachineID, "replayed", res.Replayed,
		"hostname_mismatch", res.HostnameMismatch)
	return res, nil
}

// enroll returns the failed step with any error.
func enroll(ctx context.Context, r *enrollRun, opts Options) (Result, string, error) {
	tok, err := ParseToken(opts.Token)
	if err != nil {
		return Result{}, "token", err
	}
	addr, err := HubAddress(opts.Hub)
	discover := errors.Is(err, ErrNoHub) && opts.Discover != nil
	if err != nil && !discover {
		return Result{}, "hub_address", err
	}
	if err := EnsureDir(opts.Dir); err != nil {
		return Result{}, "state", err
	}
	switch id, err := LoadIdentity(opts.Dir); {
	case err == nil:
		// A crash between saving the identity and removing the key leaves it.
		if err := removePrivate(opts.Dir, PendingKeyFile); err != nil {
			r.log.WarnContext(ctx, "enroll.failed", "step", "cleanup", "err_kind", errKind(err))
		}
		return Result{}, "already_enrolled", fmt.Errorf(
			"%w as machine %s in fleet %s; to enroll again, remove %s and keep agent.db (it holds install_id)",
			ErrAlreadyEnrolled, id.MachineID, id.FleetID, filepath.Join(opts.Dir, IdentityFile))
	case !errors.Is(err, ErrNotEnrolled):
		return Result{}, "state", err
	}
	var dial string
	if discover {
		c, err := findHub(ctx, r, opts, tok)
		if err != nil {
			return Result{}, "discover", err
		}
		addr, dial, r.peer = c.addr, c.dial, c.addr
	}
	installID, err := readInstallID(ctx, opts)
	if err != nil {
		return Result{}, "state", err
	}
	key, err := pendingKey(opts.Dir)
	if err != nil {
		return Result{}, "key", err
	}
	csr, err := newCSR(key)
	if err != nil {
		return Result{}, "key", err
	}
	facts := opts.Facts
	if facts == nil {
		facts = BasicFacts(opts.Version)
	}
	// Capabilities are first sent in Hello (03 W1); the agent has none to
	// declare at enroll yet.
	msg, err := call(ctx, r, opts, addr, dial, tok, &agentv1.EnrollRequest{
		Token: tok.Reveal(), CsrPem: csr, Facts: facts,
		ProtoMin: protoversion.Min, ProtoMax: protoversion.Max, InstallId: installID,
	})
	if err != nil {
		return Result{}, "call", err
	}
	id, err := checkResponse(msg, key, tok.CAFingerprint, time.Now())
	if err != nil {
		return Result{}, "response", err
	}
	if err := saveHubAddress(opts.Dir, addr); err != nil {
		return Result{}, "save", err
	}
	if err := saveIdentity(opts.Dir, id); err != nil {
		return Result{}, "save", err
	}
	// A new identity clears the end an earlier one stopped on; a leftover
	// for another machine id is also removed by the next run.
	if err := removePrivate(opts.Dir, EndedFile); err != nil {
		r.log.WarnContext(ctx, "enroll.failed", "step", "cleanup", "err_kind", errKind(err))
	}
	// The last identity's hub list would replace the one just enrolled.
	if err := removePrivate(opts.Dir, HubAddressesFile); err != nil {
		r.log.WarnContext(ctx, "enroll.failed", "step", "cleanup", "err_kind", errKind(err))
	}
	if err := removePrivate(opts.Dir, PendingKeyFile); err != nil {
		// The identity is saved; the leftover holds the same key, mode 0600,
		// and the next enroll on this folder removes it.
		r.log.WarnContext(ctx, "enroll.failed", "step", "cleanup", "err_kind", errKind(err))
	}
	host := facts.GetHostname()
	return Result{
		Identity: id, Replayed: msg.GetReplayed(), HubAddress: addr, Discovered: discover, Hostname: host,
		HostnameMismatch: id.ExpectedHostname != "" && hostname.Mismatch(id.ExpectedHostname, host),
	}, "", nil
}

// readInstallID opens agent.db, which mints install_id once (02g), and
// reads it.
func readInstallID(ctx context.Context, opts Options) (string, error) {
	db, err := ring.Open(ctx, opts.Dir, ring.Options{Telemetry: opts.Telemetry})
	if err != nil {
		if errors.Is(err, ring.ErrLooseMode) {
			return "", fmt.Errorf("%w: %w", ErrLooseMode, err)
		}
		return "", err
	}
	st, err := db.State(ctx)
	return st.InstallID, errors.Join(err, db.Close())
}

// failure logs a failed Enroll: the hub's refusal with its reason, an
// untrusted hub, or the agent's own step with an err_kind. No line carries
// the token, the key, the salt or the facts.
func (r *enrollRun) failure(ctx context.Context, step string, err error, hub string) {
	var re *RefusedError
	switch {
	case errors.As(err, &re):
		r.log.InfoContext(ctx, "enroll.refused", "reason", reasonName(re.Reason), "peer", peerOf(hub))
	case errors.Is(err, ErrHubUntrusted):
		r.log.WarnContext(ctx, "enroll.hub_untrusted", "peer", peerOf(hub))
	default:
		r.log.ErrorContext(ctx, "enroll.failed", "step", step, "err_kind", errKind(err))
	}
}

// errKind is the closed err_kind class of 05b.
func errKind(err error) string {
	var ne net.Error
	var nh *noHubError
	switch {
	case errors.As(err, &nh):
		return nh.kind
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled),
		errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, ErrTokenMalformed), errors.Is(err, ErrNoHub), errors.Is(err, ErrHubAddress),
		errors.Is(err, ErrBadResponse):
		return "parse"
	case errors.Is(err, ErrAlreadyEnrolled), errors.Is(err, ErrLooseMode):
		return "constraint"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused"
	default:
		return "io"
	}
}

func reasonName(r typev1.ReasonCode) string {
	return strings.TrimPrefix(r.String(), "REASON_CODE_")
}

// peerOf is the hub address as normalized, or empty if it is not valid.
func peerOf(hub string) string {
	addr, _ := HubAddress(hub)
	return addr
}
