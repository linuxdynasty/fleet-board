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
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/hub/system"
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
	// Revoker stages certificate serials onto the revoked-serials file
	// and the in-memory set before RevokeMachine's store write (02g
	// write order). Nil fails RevokeMachine closed; the daemon passes
	// the hub's revocation list.
	Revoker Revoker
	// ByeMachine ends a machine's live Connect stream (true when it
	// held one). RevokeMachine calls it after commit so a revoked
	// machine stops receiving commands at once; nil fails the call
	// closed. The daemon passes the agent listener's stream cut.
	ByeMachine func(machineID string) bool
	// OnCommandsClosed takes the outbox rows a revoke closed, for the
	// outbox service to log and count after commit. Nil skips it. The
	// daemon passes the agent listener's outbox service.
	OnCommandsClosed func(ctx context.Context, closed []outbox.Closed)
	// HubInstanceID is the hub_instance_id the gateway sends on the agent
	// wire (HelloAck): random per process start. GetServerInfo reports it
	// so a new id invalidates every Watch resume token (02k).
	HubInstanceID string
	// SystemLoop is the hub evaluation loop (issue #41): nil serves every
	// hub signal UNKNOWN with no readings, as before the loop lands. The
	// daemon passes the loop its system_evaluate job ticks.
	SystemLoop *system.Loop
}

// Fleet is the hub's FleetService. The caller comes from [PrincipalFrom],
// set by the server in front of it; the secret comes from the
// X-Fleet-Approval-Secret header ([SecretHeader]).
type Fleet struct {
	hubv1connect.UnimplementedFleetServiceHandler

	store            *store.Store
	caDER            []byte
	fingerprint      string
	tel              *telemetry.Telemetry
	now              func() time.Time
	random           io.Reader
	stateDir         string
	localTrusted     bool
	revoker          Revoker
	byeMachine       func(machineID string) bool
	onCommandsClosed func(ctx context.Context, closed []outbox.Closed)
	instanceID       string
	sysLoop          *system.Loop
	// started is the hub clock when the service was built. Fleet is
	// built once per fleetd run, so it stands in for the process
	// start GetStoreStatus reports as hub_start_time.
	started        time.Time
	tokens         metric.Int64Counter
	sessCmds       metric.Int64Counter
	approvals      metric.Int64Counter
	repos          metric.Int64Counter
	workItems      metric.Int64ObservableGauge
	sysSignalState metric.Float64ObservableGauge
	sysSignalValue metric.Float64ObservableGauge
}

// NewFleet builds the service.
func NewFleet(opts FleetOptions) (*Fleet, error) {
	if opts.Store == nil || len(opts.CACert) == 0 || opts.Telemetry == nil {
		return nil, errors.New("api: NewFleet needs a store, the CA certificate and telemetry")
	}
	if opts.HubInstanceID == "" {
		return nil, errors.New("api: NewFleet needs the hub instance id")
	}
	f := &Fleet{
		store: opts.Store, caDER: opts.CACert, tel: opts.Telemetry, now: opts.Now, random: opts.Random,
		revoker: opts.Revoker, byeMachine: opts.ByeMachine, onCommandsClosed: opts.OnCommandsClosed,
		instanceID: opts.HubInstanceID, sysLoop: opts.SystemLoop,
	}
	if f.now == nil {
		f.now = time.Now
	}
	f.started = f.now()
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
	f.repos, err = f.tel.Meter(scope).Int64Counter(f.tel.MetricPrefix()+"_repo_writes_total",
		metric.WithDescription("Repo writes by op and outcome: created, updated or deleted; replayed, refused or failed."))
	if err != nil {
		return nil, fmt.Errorf("api: repo counter: %w", err)
	}
	f.workItems, err = f.tel.Meter(scope).Int64ObservableGauge(f.tel.MetricPrefix()+"_work_items",
		metric.WithDescription("Synced work items by integration and hub stage (03m)."))
	if err != nil {
		return nil, fmt.Errorf("api: work items gauge: %w", err)
	}
	if _, err = f.tel.Meter(scope).RegisterCallback(f.observeWorkItems, f.workItems); err != nil {
		return nil, fmt.Errorf("api: work items gauge: %w", err)
	}
	if err := f.registerSystemGauges(); err != nil {
		return nil, err
	}
	return f, nil
}

// observeWorkItems reports the mirror counts by integration and stage for
// the fleet_hub_work_items gauge (03m). A store the scrape cannot read
// reports nothing; the read path's own work_items.failed covers the
// diagnosis. Stage labels are the closed 02c short names.
func (f *Fleet) observeWorkItems(ctx context.Context, o metric.Observer) error {
	for _, c := range workItemStageCounts(ctx, f.store) {
		stage := "UNKNOWN"
		if _, ok := hubv1.WorkStage_name[c.Stage]; ok {
			stage = workStageShort(c.Stage)
		}
		o.ObserveInt64(f.workItems, c.Count, metric.WithAttributes(
			attribute.String("integration", c.Integration),
			attribute.String("stage", stage)))
	}
	return nil
}

// workItemStageCounts returns the mirror counts for the gauge, empty when
// the scrape cannot read the store.
func workItemStageCounts(ctx context.Context, s *store.Store) []statedb.WorkItemStageCount {
	counts, err := statedb.CountWorkItemsByStage(ctx, s)
	if err != nil {
		return nil
	}
	return counts
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
			"the previous machine is neither revoked nor past its certificate lifetime", nil, nil))
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
