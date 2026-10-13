package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

const scope = "github.com/linuxdynasty/fleet-board/internal/hub/api"

// MaxMessageBytes caps a request message (02e: the 1 MiB Connect cap).
const MaxMessageBytes = 1 << 20

// FleetOptions build [NewFleet]. Store, CACert and Telemetry are required.
type FleetOptions struct {
	Store *store.Store
	// CACert is the fleet CA certificate (DER) from hub_identity.
	CACert    []byte
	Telemetry *telemetry.Telemetry
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Random is the source of token secrets; nil means crypto/rand.
	Random io.Reader
	// StateDir holds the approval secret file (secret.Path); empty fails
	// the secret check closed. Set from the hub config's state dir.
	StateDir string
	// LocalTrusted is the hub's auth mode (config.Mode): in local_trusted
	// mode DecideApproval and the SETTINGS and ADMIN calls need the local
	// approval secret as well (ADR 0032).
	LocalTrusted bool
}

// Fleet is the hub's FleetService. The caller comes from [PrincipalFrom],
// set by the server in front of it; the secret comes from the
// X-Fleet-Approval-Secret header ([SecretHeader]).
type Fleet struct {
	hubv1connect.UnimplementedFleetServiceHandler

	store        *store.Store
	caDER        []byte
	fingerprint  string
	tel          *telemetry.Telemetry
	now          func() time.Time
	random       io.Reader
	stateDir     string
	localTrusted bool
	tokens       metric.Int64Counter
	sessCmds     metric.Int64Counter
	approvals    metric.Int64Counter
}

// NewFleet builds the service.
func NewFleet(opts FleetOptions) (*Fleet, error) {
	if opts.Store == nil || len(opts.CACert) == 0 || opts.Telemetry == nil {
		return nil, errors.New("api: NewFleet needs a store, the CA certificate and telemetry")
	}
	f := &Fleet{store: opts.Store, caDER: opts.CACert, tel: opts.Telemetry, now: opts.Now, random: opts.Random}
	if f.now == nil {
		f.now = time.Now
	}
	if f.random == nil {
		f.random = rand.Reader
	}
	fp := sha256.Sum256(opts.CACert)
	f.fingerprint = hex.EncodeToString(fp[:])
	f.stateDir = opts.StateDir
	f.localTrusted = opts.LocalTrusted
	var err error
	f.tokens, err = f.tel.Meter(scope).Int64Counter(f.tel.MetricPrefix()+"_enroll_tokens_total",
		metric.WithDescription("CreateEnrollmentToken calls, by result: issued, refused (the caller's error) or failed (the hub's)."))
	if err != nil {
		return nil, fmt.Errorf("api: enroll token counter: %w", err)
	}
	f.sessCmds, err = f.tel.Meter(scope).Int64Counter(f.tel.MetricPrefix()+"_session_commands_total",
		metric.WithDescription("Session-control calls by agent command kind and result: sent, replayed, refused or failed."))
	if err != nil {
		return nil, fmt.Errorf("api: session command counter: %w", err)
	}
	f.approvals, err = f.tel.Meter(scope).Int64Counter(f.tel.MetricPrefix()+"_approvals_total",
		metric.WithDescription("Approvals by kind and outcome: created, approved, denied, expired or withdrawn."))
	if err != nil {
		return nil, fmt.Errorf("api: approval counter: %w", err)
	}
	return f, nil
}

// Handler is the Connect route of the service, for a mux.
func (f *Fleet) Handler() (string, http.Handler) {
	return hubv1connect.NewFleetServiceHandler(f, connect.WithReadMaxBytes(MaxMessageBytes))
}

// CreateEnrollmentToken mints an `fbe1_` token for the stored CA and stores
// only the SHA-256 of its secret (02b, 02g), with its machine.enroll_token
// audit row naming the caller in the same transaction (02m; never the
// token). Order of checks, as 02b ranks them: the caller, the fields, the
// request_id, then previous_machine.
func (f *Fleet) CreateEnrollmentToken(ctx context.Context, req *connect.Request[hubv1.CreateEnrollmentTokenRequest],
) (*connect.Response[hubv1.CreateEnrollmentTokenResponse], error) {
	who, err := authorizeAdmin(ctx)
	if err != nil {
		return nil, f.refused(ctx, "", err)
	}
	actor := who.GetId().GetValue()
	// ADMIN scope, so the local approval secret is needed in local_trusted
	// mode too (ADR 0032); the check attaches to each further SETTINGS and
	// ADMIN call as its row lands, from RequiresLocalSecret.
	if err := f.checkLocalSecret(hubProcedure("CreateEnrollmentToken"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.refused(ctx, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	ttl, err := checkEnrollRequest(m)
	if err != nil {
		return nil, f.refused(ctx, actor, err)
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return nil, f.failed(ctx, actor, "encode", err)
	}
	tok, err := ca.NewToken(f.caDER, f.random)
	if err != nil {
		return nil, f.failed(ctx, actor, "random", err)
	}
	now := f.now().UTC().Truncate(time.Millisecond)
	notAfter := now.Add(ttl)
	err = statedb.InsertEnrollToken(ctx, f.store, statedb.EnrollToken{
		SecretHash: tok.SecretHash, MachineName: m.GetMachineName(), ExpectedHostname: m.GetExpectedHostname(),
		Labels: m.GetLabels(), StartDrained: m.GetStartDrained(), NotAfter: notAfter,
		PreviousMachine: m.GetPreviousMachine().GetValue(), Created: now,
		Principal: actor, RequestID: m.GetRequestId(), RequestHash: sha256.Sum256(body),
		Actor: statedb.Actor{
			ID: actor, Kind: strings.TrimPrefix(who.GetKind().String(), "PRINCIPAL_KIND_"), Name: who.GetName(),
		},
	})
	switch {
	case errors.Is(err, statedb.ErrSecretDelivered):
		return nil, f.refused(ctx, actor, apiError(connect.CodeAlreadyExists, typev1.ReasonCode_REASON_CODE_SECRET_ALREADY_DELIVERED,
			"this request_id already delivered its token, which is never stored; make a new request", nil, nil))
	case errors.Is(err, statedb.ErrRequestConflict):
		return nil, f.refused(ctx, actor, apiError(connect.CodeAlreadyExists, typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil))
	case errors.Is(err, statedb.ErrNotReenrollable):
		return nil, f.refused(ctx, actor, apiError(connect.CodeFailedPrecondition, typev1.ReasonCode_REASON_CODE_MACHINE_NOT_REENROLLABLE,
			"re-enrolling a known machine is not built yet", nil, nil))
	case err != nil:
		return nil, f.failed(ctx, actor, "store", err)
	}
	f.tokens.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "issued")))
	f.tel.Logger.InfoContext(ctx, "enroll.issued", "machine", m.GetMachineName(),
		"expire_time", notAfter.Format(time.RFC3339), "actor", actor)
	text := tok.Reveal()
	return connect.NewResponse(&hubv1.CreateEnrollmentTokenResponse{
		Token:         text,
		ExpireTime:    timestamppb.New(notAfter),
		CaFingerprint: f.fingerprint,
		EnrollCommand: "fleet-agent enroll --token " + text,
	}), nil
}

// refused counts and logs a caller error by its reason and returns it.
func (f *Fleet) refused(ctx context.Context, actor string, err error) error {
	reason := "UNSPECIFIED"
	var ce *connect.Error
	if errors.As(err, &ce) {
		for _, d := range ce.Details() {
			if v, derr := d.Value(); derr == nil {
				if ed, ok := v.(*typev1.ErrorDetail); ok {
					reason = strings.TrimPrefix(ed.GetReason().String(), "REASON_CODE_")
				}
			}
		}
	}
	f.tokens.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "refused")))
	f.tel.Logger.InfoContext(ctx, "enroll.issue_refused", "reason", reason, "actor", actor)
	return err
}

// failed counts and logs the hub's own failure at step (a closed set:
// encode, random, store) and returns INTERNAL without the cause's text.
func (f *Fleet) failed(ctx context.Context, actor, step string, cause error) error {
	f.tokens.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "failed")))
	kind := "io"
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		kind = "timeout"
	}
	f.tel.Logger.ErrorContext(ctx, "enroll.issue_failed", "step", step, "err_kind", kind, "actor", actor)
	return apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL, "the hub could not create the token", nil, nil)
}
