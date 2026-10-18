package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// APIVersion is the highest fleet.hub.v1 minor revision served (02k:
// GetServerInfoResponse.api_version). It moves when a row adds a field or
// call clients must know about; contracts stay unfrozen until v0.1.
const APIVersion = 1

var _ hubv1connect.FleetServiceHandler = (*Fleet)(nil)

// GetServerInfo is the handshake read (common.proto: allowed without scopes,
// so a client can check the API before pairing). It names the hub, what it
// serves, and the caller as the hub sees it. features holds only optional
// capabilities; none is built yet, so it is empty (02k: exactly what is
// served). The one store read is the hub_identity row; a store without one
// or one that fails to read is the hub's failure, INTERNAL with no detail.
func (f *Fleet) GetServerInfo(ctx context.Context, _ *connect.Request[hubv1.GetServerInfoRequest],
) (*connect.Response[hubv1.GetServerInfoResponse], error) {
	id, err := statedb.ReadIdentity(ctx, f.store)
	if err != nil {
		kind := "io"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			kind = "timeout"
		}
		f.tel.Logger.ErrorContext(ctx, "server_info.failed", "step", "identity", "err_kind", kind)
		return nil, apiError(connect.CodeInternal, typev1.ReasonCode_REASON_CODE_INTERNAL,
			"the hub could not read its identity", nil, nil)
	}
	if err := quotaCheckpoint(ctx, quotaPublic); err != nil {
		return nil, err
	}
	mode := hubv1.AuthMode_AUTH_MODE_AUTHENTICATED
	if f.localTrusted {
		mode = hubv1.AuthMode_AUTH_MODE_LOCAL_TRUSTED
	}
	caller := PrincipalFrom(ctx)
	if _, err := authorizeScope(ctx, "READ"); err != nil {
		caller = nil
	}
	return connect.NewResponse(&hubv1.GetServerInfoResponse{
		ApiVersion: APIVersion, HubVersion: buildinfo.Version,
		FleetId: id.FleetID, HubInstanceId: f.instanceID, ServerTime: timestamppb.New(f.hubNow()),
		AuthMode: mode, Caller: caller, CallerScopes: scopesOf(caller),
		// ClaimPending stays false: owner claim arrives with authenticated
		// mode (v3), which the hub refuses to start in (02k).
		ClaimPending: false,
	}), nil
}

// scopesOf is the 02d role-to-scope map for a caller. A principal the hub
// does not know (nil, or a role that never calls the API like SCHEDULER)
// holds no scope. extra_scopes arrive with authenticated mode (02d); on the
// admin socket the role is the whole story.
func scopesOf(p *typev1.Principal) []hubv1.Scope {
	switch p.GetRole() {
	case typev1.Role_ROLE_VIEWER:
		return []hubv1.Scope{hubv1.Scope_SCOPE_READ}
	case typev1.Role_ROLE_OPERATOR:
		return []hubv1.Scope{hubv1.Scope_SCOPE_READ, hubv1.Scope_SCOPE_SESSIONS_WRITE, hubv1.Scope_SCOPE_APPROVALS}
	case typev1.Role_ROLE_OWNER:
		return []hubv1.Scope{
			hubv1.Scope_SCOPE_READ, hubv1.Scope_SCOPE_SESSIONS_WRITE, hubv1.Scope_SCOPE_APPROVALS,
			hubv1.Scope_SCOPE_SETTINGS, hubv1.Scope_SCOPE_ADMIN, hubv1.Scope_SCOPE_LOGS,
		}
	default:
		return nil
	}
}
