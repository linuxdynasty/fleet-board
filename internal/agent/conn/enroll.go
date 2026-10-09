package conn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1/agentv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/ring"
	"github.com/linuxdynasty/fleet-board/internal/protoversion"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const (
	scope = "github.com/linuxdynasty/fleet-board/internal/agent/conn"
	// DefaultTimeout bounds one Enroll call, handshake included.
	DefaultTimeout = 30 * time.Second
	// maxMessageBytes is the 1 MiB message cap of 02e.
	maxMessageBytes = 1 << 20
	maxHubMessage   = 200
)

var (
	// ErrAlreadyEnrolled means the state folder already holds an identity.
	// Enroll never replaces it; to move a machine to a new identity the owner
	// removes the folder first (02g machine lifecycle).
	ErrAlreadyEnrolled = errors.New("conn: this machine is already enrolled")
	// ErrHubUntrusted means the TLS check against the CA in the token failed:
	// nothing was sent (02g A1 step 2).
	ErrHubUntrusted = errors.New("conn: the hub's certificate does not match the CA in the token; nothing was sent")
	// ErrBadResponse means the hub's EnrollResponse failed the agent's checks;
	// nothing was saved and the pending key is kept for a retry.
	ErrBadResponse = errors.New("conn: the hub's enroll response is not valid")
)

// RefusedError is an Enroll error the hub answered with: its code, the
// ErrorDetail reason and retryable flag (02g), and the hub's message.
type RefusedError struct {
	Code      connect.Code
	Reason    typev1.ReasonCode
	Retryable bool
	Message   string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("the hub refused to enroll this machine: %s (%s): %s", reasonName(e.Reason), e.Code, e.Message)
}

// Options are the inputs of [Enroll].
type Options struct {
	// Dir is the agent state folder (04 Q9); see [DefaultDir].
	Dir string
	// Hub is the hub address, host or host:port. Empty is [ErrNoHub] until
	// discovery exists.
	Hub string
	// Token is the enroll token text the owner pasted.
	Token string
	// Version is the agent version, sent in the facts.
	Version string
	// Facts default to [BasicFacts].
	Facts     *agentv1.MachineFacts
	Telemetry *telemetry.Telemetry
	// Timeout bounds the Enroll call; 0 means [DefaultTimeout].
	Timeout time.Duration
}

// Result is a finished enrollment.
type Result struct {
	Identity Identity
	// Replayed is true when the hub returned its stored answer to an
	// earlier attempt with the same token, install_id and key.
	Replayed bool
	// HubAddress is the address enrolled with, now cached.
	HubAddress string
}

// enrollRun carries one Enroll's logger and span.
type enrollRun struct {
	log  *slog.Logger
	span trace.Span
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
	ctx, span := opts.Telemetry.Tracer(scope).Start(ctx, "agent.enroll")
	defer span.End()
	r := &enrollRun{log: opts.Telemetry.Logger.With("component", "conn"), span: span}
	res, step, err := enroll(ctx, r, opts)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, step)
		r.failure(ctx, step, err, opts.Hub)
		return Result{}, err
	}
	r.log.InfoContext(ctx, "enroll.completed", "machine_id", res.Identity.MachineID, "replayed", res.Replayed)
	return res, nil
}

// enroll returns the failed step with any error.
func enroll(ctx context.Context, r *enrollRun, opts Options) (Result, string, error) {
	tok, err := ParseToken(opts.Token)
	if err != nil {
		return Result{}, "token", err
	}
	addr, err := HubAddress(opts.Hub)
	if err != nil {
		return Result{}, "hub_address", err
	}
	if err := EnsureDir(opts.Dir); err != nil {
		return Result{}, "state", err
	}
	switch id, err := LoadIdentity(opts.Dir); {
	case err == nil:
		return Result{}, "already_enrolled", fmt.Errorf("%w as machine %s in fleet %s; remove %s to enroll again",
			ErrAlreadyEnrolled, id.MachineID, id.FleetID, opts.Dir)
	case !errors.Is(err, ErrNotEnrolled):
		return Result{}, "state", err
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
	msg, err := call(ctx, opts, addr, tok, &agentv1.EnrollRequest{
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
	if err := removePrivate(opts.Dir, PendingKeyFile); err != nil {
		// The identity is saved; the leftover holds the same key, mode 0600.
		r.log.WarnContext(ctx, "enroll.failed", "step", "cleanup", "err_kind", errKind(err))
	}
	return Result{Identity: id, Replayed: msg.GetReplayed(), HubAddress: addr}, "", nil
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

// call sends EnrollRequest over TLS pinned to the token's CA.
func call(ctx context.Context, opts Options, addr string, tok Token, req *agentv1.EnrollRequest,
) (*agentv1.EnrollResponse, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %q", ErrHubAddress, addr)
	}
	var untrusted atomic.Bool
	tr := &http.Transport{
		TLSClientConfig:        pinnedTLS(tok.CAFingerprint, host, &untrusted),
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
	defer tr.CloseIdleConnections()
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := agentv1connect.NewAgentServiceClient(&http.Client{Transport: tr, Timeout: timeout}, "https://"+addr,
		connect.WithGRPC(), connect.WithReadMaxBytes(maxMessageBytes), connect.WithSendMaxBytes(maxMessageBytes))
	res, err := client.Enroll(ctx, connect.NewRequest(req))
	switch {
	case untrusted.Load():
		return nil, fmt.Errorf("%w (%s)", ErrHubUntrusted, addr)
	case err != nil:
		var ce *connect.Error
		if errors.As(err, &ce) && len(ce.Details()) > 0 {
			return nil, refused(ce)
		}
		return nil, fmt.Errorf("conn: enroll call to %s: %w", addr, err)
	}
	return res.Msg, nil
}

// refused builds a RefusedError from the hub's Connect error and its
// ErrorDetail.
func refused(ce *connect.Error) *RefusedError {
	e := &RefusedError{Code: ce.Code(), Message: clean(ce.Message(), maxHubMessage)}
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if ed, ok := v.(*typev1.ErrorDetail); ok {
				e.Reason, e.Retryable = ed.GetReason(), ed.GetRetryable()
			}
		}
	}
	return e
}

// failure logs a failed Enroll: the hub's refusal with its reason, an
// untrusted hub, or the agent's own step with an err_kind. No line carries
// the token, the key, the salt or the facts.
func (r *enrollRun) failure(ctx context.Context, step string, err error, hub string) {
	var re *RefusedError
	switch {
	case errors.As(err, &re):
		r.log.InfoContext(ctx, "enroll.refused", "reason", reasonName(re.Reason), "peer", peerOf(hub),
			"retryable", re.Retryable)
	case errors.Is(err, ErrHubUntrusted):
		r.log.WarnContext(ctx, "enroll.hub_untrusted", "peer", peerOf(hub))
	default:
		r.log.ErrorContext(ctx, "enroll.failed", "step", step, "err_kind", errKind(err))
	}
}

// errKind is the closed err_kind class of 05b.
func errKind(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled),
		errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, ErrTokenMalformed), errors.Is(err, ErrNoHub), errors.Is(err, ErrHubAddress),
		errors.Is(err, ErrBadResponse):
		return "parse"
	case errors.Is(err, ErrAlreadyEnrolled), errors.Is(err, ErrLooseMode):
		return "constraint"
	case strings.Contains(err.Error(), "connection refused"):
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
