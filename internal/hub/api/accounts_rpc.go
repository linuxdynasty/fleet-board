package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strconv"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// Account field rules (account.proto, 02b "Size limits").
var (
	// providerName is the CreateAccountRequest.provider rule: 1 to 64
	// bytes of [a-z0-9-].
	providerName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
)

const (
	maxHarnessName = 64
	maxProfileName = 128
	// maxReserve is the AccountSpec.reserve_percent cap: 0 to 50
	// (defaults.md, ADR 0024).
	maxReserve = 50
	// defaultReserve is the quota a new account keeps for interactive
	// use (account.proto).
	defaultReserve = 10
)

// defaultConsent is what a new account starts with (account.proto,
// ADR 0008): push, pr-open and merge.
var defaultConsent = []typev1.ConsentAct{
	typev1.ConsentAct_CONSENT_ACT_PUSH,
	typev1.ConsentAct_CONSENT_ACT_PR_OPEN,
	typev1.ConsentAct_CONSENT_ACT_MERGE,
}

// updateAccountAllow is the UpdateAccount mask allowlist: the
// owner-editable spec fields (account.proto). Harness, provider and
// status stay create-time and observed.
var updateAccountAllow = map[string]bool{
	"spec.display_name":     true,
	"spec.mode":             true,
	"spec.reserve_percent":  true,
	"spec.consent":          true,
	"spec.budget_kind":      true,
	"spec.spend_cap_micros": true,
	"spec.bindings":         true,
}

// newAccountID mints the hub's opaque account id: 16 random bytes as
// lowercase hex (ids.proto AccountId).
func newAccountID(r io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// checkAccountSpec applies the 02b spec rules to an account spec:
// names fit, the reserve stays within 100, consent names known acts,
// bindings name a machine. Descriptions never echo the request.
func checkAccountSpec(spec *hubv1.AccountSpec) error {
	var v violations
	if spec == nil {
		v.add("spec", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "spec is required")
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	switch name := spec.GetDisplayName(); {
	case name == "":
		v.add("spec.display_name", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "spec.display_name is required")
	case len(name) > maxDisplayName:
		v.add("spec.display_name", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "spec.display_name is over 128 bytes")
	case !displayText(name):
		v.add("spec.display_name", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
			"spec.display_name must be UTF-8 without control or invisible format characters")
	}
	switch spec.GetMode() {
	case hubv1.AccountMode_ACCOUNT_MODE_FLEET, hubv1.AccountMode_ACCOUNT_MODE_INTERACTIVE_ONLY:
	default:
		v.add("spec.mode", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "spec.mode names a known account mode")
	}
	if r := spec.GetReservePercent(); r > maxReserve {
		v.add("spec.reserve_percent", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"spec.reserve_percent is 0 to 50")
	}
	for i, c := range spec.GetConsent() {
		switch c {
		case typev1.ConsentAct_CONSENT_ACT_PUSH,
			typev1.ConsentAct_CONSENT_ACT_PR_OPEN,
			typev1.ConsentAct_CONSENT_ACT_MERGE:
		default:
			v.add("spec.consent["+strconv.Itoa(i)+"]", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"spec.consent names push, pr-open or merge")
		}
	}
	switch spec.GetBudgetKind() {
	case typev1.BudgetKind_BUDGET_KIND_SUBSCRIPTION_PERCENT, typev1.BudgetKind_BUDGET_KIND_DOLLAR_CAP:
	default:
		v.add("spec.budget_kind", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "spec.budget_kind names a known budget kind")
	}
	if spec.GetSpendCapMicros() < 0 {
		v.add("spec.spend_cap_micros", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"spec.spend_cap_micros is 0 for no limit, never negative")
	}
	for i, b := range spec.GetBindings() {
		field := "spec.bindings[" + strconv.Itoa(i) + "]"
		if b.GetMachine().GetValue() == "" {
			v.add(field+".machine", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"a binding names the machine it maps")
		}
		if p := b.GetProfile(); len(p) > maxProfileName || !displayText(p) {
			v.add(field+".profile", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"a binding profile is at most 128 bytes without control characters")
		}
	}
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}

// checkCreateAccount applies the create-only rules: the harness and
// provider identity, and the dollar-cap budget of an API-key account
// (account.proto). It fills the stated defaults on a clone.
func checkCreateAccount(harness, provider string, spec *hubv1.AccountSpec) (*hubv1.AccountSpec, error) {
	var v violations
	switch {
	case harness == "":
		v.add("harness", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "harness is required")
	case len(harness) > maxHarnessName || !displayText(harness):
		v.add("harness", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"harness is at most 64 bytes without control characters")
	}
	if !providerName.MatchString(provider) {
		v.add("provider", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"provider is 1 to 64 bytes of a-z, 0-9 and '-'")
	}
	if spec.GetBudgetKind() != typev1.BudgetKind_BUDGET_KIND_DOLLAR_CAP {
		v.add("spec.budget_kind", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"an owner-created account bills by the dollar")
	}
	if len(v) > 0 {
		return nil, apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	out := proto.Clone(spec).(*hubv1.AccountSpec) //nolint:forcetypeassert // Clone keeps the message type
	// Presence carries the owner's choice: only an unset reserve takes the
	// default, while an explicit zero stays zero.
	if out.ReservePercent == nil {
		out.ReservePercent = proto.Uint32(defaultReserve)
	}
	if len(out.GetConsent()) == 0 {
		out.Consent = append([]typev1.ConsentAct(nil), defaultConsent...)
	}
	if err := checkAccountSpec(out); err != nil {
		return nil, err
	}
	return out, nil
}

// accountFromStored answers the Account of a stored row with spec. Status
// carries what the owner set (harness, provider, origin); usage, pacing
// and the probe are computed by the reads (row 12), so writes leave them
// unset.
func accountFromStored(stored statedb.AccountStored, spec *hubv1.AccountSpec) *hubv1.Account {
	return &hubv1.Account{
		Id:         &typev1.AccountId{Value: stored.ID},
		Version:    stored.Version,
		CreateTime: timestamppb.New(millisTime(stored.CreateMs)),
		UpdateTime: timestamppb.New(millisTime(stored.UpdateMs)),
		Spec:       spec,
		Status: &hubv1.AccountStatus{
			Harness:  &typev1.Harness{Name: stored.Harness},
			Provider: stored.Provider,
			Origin:   hubv1.AccountOrigin_ACCOUNT_ORIGIN_OWNER_CREATED,
		},
	}
}

// accountBodyHash is SHA-256 over the deterministic request bytes, to
// tell a retry from a conflict.
func accountBodyHash(m proto.Message) ([32]byte, error) {
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(body), nil
}

// accountStoreError renders an account write's store failure: the replay
// conflict, the existing or missing account, the stale version (02b:
// FAILED_PRECONDITION with VERSION_MISMATCH and
// metadata["current_version"]), then the hub's own failure as INTERNAL
// without its text. A Connect error (a merged-spec violation from the
// writer job) passes through untouched.
func accountStoreError(op string, err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	var stale *statedb.VersionStale
	switch {
	case errors.Is(err, statedb.ErrRequestConflict):
		return apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT,
			"this request_id was used for a different request", nil, nil)
	case errors.Is(err, statedb.ErrAccountExists):
		return apiError(connect.CodeAlreadyExists,
			typev1.ReasonCode_REASON_CODE_RESOURCE_ALREADY_EXISTS,
			"this harness already has this account", nil, nil)
	case errors.Is(err, statedb.ErrAccountUnknown):
		return apiError(connect.CodeNotFound,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
			"no account has this id", nil, nil)
	case errors.As(err, &stale):
		return apiError(connect.CodeFailedPrecondition,
			typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH,
			"the account changed; read it again",
			map[string]string{"current_version": strconv.FormatUint(stale.Current, 10)}, nil)
	default:
		return apiError(connect.CodeInternal,
			typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the hub could not "+op+" the account", nil, nil)
	}
}

// CreateAccount adds an owner-defined API-key account (02j, 02m). A new
// account gets reserve 10 and the push, pr-open and merge consent when
// the request leaves them unset. Order of checks, as 02b ranks them: the
// caller (SETTINGS), the local secret in local_trusted mode, the fields,
// then the request_id replay in the writer transaction.
func (f *Fleet) CreateAccount(ctx context.Context, req *connect.Request[hubv1.CreateAccountRequest],
) (*connect.Response[hubv1.CreateAccountResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.configOpRefused(ctx, opAccountCreate, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("CreateAccount"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.configOpRefused(ctx, opAccountCreate, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.configOpRefused(ctx, opAccountCreate, actor, err)
	}
	spec, err := checkCreateAccount(m.GetHarness().GetName(), m.GetProvider(), m.GetSpec())
	if err != nil {
		return nil, f.configOpRefused(ctx, opAccountCreate, actor, err)
	}
	sum, err := accountBodyHash(m)
	if err != nil {
		return nil, f.configOpFailed(ctx, opAccountCreate, "create", actor, "encode", err)
	}
	id, err := newAccountID(f.random)
	if err != nil {
		return nil, f.configOpFailed(ctx, opAccountCreate, "create", actor, "encode", err)
	}
	specJSON, err := protojson.Marshal(spec)
	if err != nil {
		return nil, f.configOpFailed(ctx, opAccountCreate, "create", actor, "encode", err)
	}
	now := f.hubNow()
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	pctx := context.WithoutCancel(ctx) // the publish hook runs on the writer after the handler may have left
	var stored statedb.AccountStored
	stored, err = statedb.CreateOwnerAccount(writeCtx, f.store, statedb.CreateAccount{
		ConfigWrite: statedb.ConfigWrite{
			Principal: actor, RequestID: m.GetRequestId(),
			RequestHash: sum, Actor: actorOf(who), Now: now,
		},
		ID: id, Harness: m.GetHarness().GetName(), Provider: m.GetProvider(),
		DisplayName: spec.GetDisplayName(), Origin: statedb.AccountOriginOwner, Spec: specJSON,
	}, func(s statedb.AccountStored) {
		// Runs on the writer in commit order (#393).
		f.publishAccountWatch(pctx, s, hubv1.ChangeType_CHANGE_TYPE_CREATED, nil, s.Replay)
	})
	if err != nil {
		return nil, f.configOpWriteError(ctx, opAccountCreate, "create", actor, accountStoreError("create", err))
	}
	var outSpec hubv1.AccountSpec
	if err := protojson.Unmarshal(stored.Spec, &outSpec); err != nil {
		return nil, f.configOpFailed(ctx, opAccountCreate, "create", actor, "decode", err)
	}
	f.configOpWrote(ctx, opAccountCreate, actor, stored.ID, stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.CreateAccountResponse{
		Account: accountFromStored(stored, &outSpec),
	}), nil
}

// UpdateAccount rewrites the masked spec fields of an account (02j): the
// reserve, consent, mode and Codex bindings. Harness, provider and status
// are create-time and observed. The patch merges against the
// transaction-current row, so concurrent updates to different fields keep
// each other's changes. A stale expected_version is FAILED_PRECONDITION
// with VERSION_MISMATCH.
func (f *Fleet) UpdateAccount(ctx context.Context, req *connect.Request[hubv1.UpdateAccountRequest],
) (*connect.Response[hubv1.UpdateAccountResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.configOpRefused(ctx, opAccountUpdate, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("UpdateAccount"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.configOpRefused(ctx, opAccountUpdate, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.configOpRefused(ctx, opAccountUpdate, actor, err)
	}
	if m.GetAccount().GetId().GetValue() == "" {
		var v violations
		v.add("account.id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "account.id is required")
		return nil, f.configOpRefused(ctx, opAccountUpdate, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the request has invalid fields", nil, v))
	}
	if m.GetAccount().GetSpec() == nil {
		var v violations
		v.add("spec", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "spec is required")
		return nil, f.configOpRefused(ctx, opAccountUpdate, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the request has invalid fields", nil, v))
	}
	if err := CheckFieldMask(m.GetUpdateMask(), updateAccountAllow); err != nil {
		return nil, f.configOpRefused(ctx, opAccountUpdate, actor, err)
	}
	sum, err := accountBodyHash(m)
	if err != nil {
		return nil, f.configOpFailed(ctx, opAccountUpdate, "update", actor, "encode", err)
	}
	now := f.hubNow()
	patch, paths := m.GetAccount().GetSpec(), m.GetUpdateMask().GetPaths()
	// oldSpec keeps the pre-update spec for UNMATCHED evaluation.
	var oldSpec []byte
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	pctx := context.WithoutCancel(ctx) // the publish hook runs on the writer after the handler may have left
	var stored statedb.AccountStored
	{
		var err error
		stored, err = statedb.UpdateOwnerAccount(writeCtx, f.store, statedb.UpdateAccountOp{
			ConfigWrite: statedb.ConfigWrite{
				Principal: actor, RequestID: m.GetRequestId(),
				RequestHash: sum, Actor: actorOf(who), Now: now,
			},
			ID: inAccountID(m), ExpectedVersion: m.GetExpectedVersion(),
			Merge: func(current []byte) ([]byte, string, error) {
				oldSpec = current
				var currentSpec hubv1.AccountSpec
				if err := protojson.Unmarshal(current, &currentSpec); err != nil {
					return nil, "", err
				}
				merged, err := applyMaskedFields(&currentSpec, patch, paths, "spec")
				if err != nil {
					return nil, "", err
				}
				mergedSpec := merged.(*hubv1.AccountSpec) //nolint:forcetypeassert // applyMaskedFields clones the stored type
				// Only the merged result is validated: fields outside the
				// mask are ignored, even when the request carries invalid
				// values for them.
				if err := checkAccountSpec(mergedSpec); err != nil {
					return nil, "", err
				}
				mergedJSON, err := protojson.Marshal(mergedSpec)
				if err != nil {
					return nil, "", err
				}
				return mergedJSON, mergedSpec.GetDisplayName(), nil
			},
		}, func(s statedb.AccountStored) {
			// Runs on the writer in commit order (#393).
			f.publishAccountWatch(pctx, s, hubv1.ChangeType_CHANGE_TYPE_UPDATED, oldSpec, s.Replay)
		})
		if err != nil {
			return nil, f.configOpWriteError(ctx, opAccountUpdate, "update", actor, accountStoreError("update", err))
		}
	}
	var outSpec hubv1.AccountSpec
	if err := protojson.Unmarshal(stored.Spec, &outSpec); err != nil {
		return nil, f.configOpFailed(ctx, opAccountUpdate, "update", actor, "decode", err)
	}
	f.configOpWrote(ctx, opAccountUpdate, actor, stored.ID, stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.UpdateAccountResponse{
		Account: accountFromStored(stored, &outSpec),
	}), nil
}

// DeleteAccount removes an account nothing uses (02m): no live goal and
// no signed-in machine hold it (ACCOUNT_IN_USE otherwise). No table
// references an account yet (no dispatch rows, no discovery), so every
// delete in this slice succeeds a version check; the refusal follows
// with them. A stale expected_version is FAILED_PRECONDITION with
// VERSION_MISMATCH; deleting a missing account is NOT_FOUND.
func (f *Fleet) DeleteAccount(ctx context.Context, req *connect.Request[hubv1.DeleteAccountRequest],
) (*connect.Response[hubv1.DeleteAccountResponse], error) {
	who, err := authorizeSettings(ctx)
	if err != nil {
		return nil, f.configOpRefused(ctx, opAccountDelete, "", err)
	}
	actor := who.GetId().GetValue()
	if err := f.checkLocalSecret(hubProcedure("DeleteAccount"), req.Header().Get(SecretHeader)); err != nil {
		return nil, f.configOpRefused(ctx, opAccountDelete, actor, apiError(connect.CodePermissionDenied,
			typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"this call needs the local approval secret", nil, nil))
	}
	m := req.Msg
	if err := checkRequestID(m.GetRequestId()); err != nil {
		return nil, f.configOpRefused(ctx, opAccountDelete, actor, err)
	}
	if m.GetId().GetValue() == "" {
		var v violations
		v.add("id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "id is required")
		return nil, f.configOpRefused(ctx, opAccountDelete, actor, apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "the request has invalid fields", nil, v))
	}
	sum, err := accountBodyHash(m)
	if err != nil {
		return nil, f.configOpFailed(ctx, opAccountDelete, "delete", actor, "encode", err)
	}
	now := f.hubNow()
	writeCtx := statedb.WithValidatedWrite(ctx, func() error { return quotaCheckpoint(ctx, quotaWrite) })
	pctx := context.WithoutCancel(ctx) // the publish hook runs on the writer after the handler may have left
	var stored statedb.AccountStored
	stored, err = statedb.DeleteOwnerAccount(writeCtx, f.store, statedb.DeleteAccountOp{
		ConfigWrite: statedb.ConfigWrite{
			Principal: actor, RequestID: m.GetRequestId(),
			RequestHash: sum, Actor: actorOf(who), Now: now,
		},
		ID: m.GetId().GetValue(), ExpectedVersion: m.GetExpectedVersion(),
	}, func(s statedb.AccountStored) {
		// The row keeps the last state with the delete's own
		// stamp, so a resume replays the DELETED in order.
		// Runs on the writer in commit order (#393).
		f.publishAccountWatch(pctx, s, hubv1.ChangeType_CHANGE_TYPE_DELETED, nil, s.Replay)
	})
	if err != nil {
		return nil, f.configOpWriteError(ctx, opAccountDelete, "delete", actor, accountStoreError("delete", err))
	}
	// A replayed delete answers the same empty outcome.
	f.configOpWrote(ctx, opAccountDelete, actor, stored.ID, stored.Version, stored.Replay)
	return connect.NewResponse(&hubv1.DeleteAccountResponse{}), nil
}

// inAccountID is the account identity of an update request.
func inAccountID(m *hubv1.UpdateAccountRequest) string {
	return m.GetAccount().GetId().GetValue()
}
