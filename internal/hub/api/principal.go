package api

import (
	"context"
	"errors"
	"os/user"
	"regexp"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

type (
	principalKey           struct{}
	localOwnerAdmissionKey struct{}
)

// WithPrincipal is ctx carrying who is calling. The server in front of the
// FleetService handler sets it (the admin socket from the peer's kernel
// credentials); a handler never takes it from the request.
func WithPrincipal(ctx context.Context, p *typev1.Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom is the caller set by [WithPrincipal], or nil.
func PrincipalFrom(ctx context.Context) *typev1.Principal {
	p, _ := ctx.Value(principalKey{}).(*typev1.Principal)
	return p
}

// withLocalOwner is called only by the checked local TCP listener. The
// private marker prevents a caller that merely supplies the same principal
// fields through WithPrincipal from gaining the local owner's scopes.
func withLocalOwner(ctx context.Context) context.Context {
	p := &typev1.Principal{
		Id: &typev1.PrincipalId{Value: "local-owner"}, Name: "owner",
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER, Role: typev1.Role_ROLE_OWNER,
	}
	return context.WithValue(WithPrincipal(ctx, p), localOwnerAdmissionKey{}, true)
}

// authorizeScope keeps all v1 RPC families on the same admission rule. The
// admin socket's LOCAL_USER comes from its kernel peer check. The TCP USER
// must carry the private marker minted after the listener checks its peer,
// Host, Origin and credential carriers.
func authorizeScope(ctx context.Context, scope string) (*typev1.Principal, error) {
	p := PrincipalFrom(ctx)
	if p.GetId().GetValue() == "" {
		return nil, apiError(connect.CodeUnauthenticated, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID,
			"no caller identity", nil, nil)
	}
	local := p.GetKind() == typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER &&
		strings.HasPrefix(p.GetId().GetValue(), "local:") && p.GetRole() == typev1.Role_ROLE_OWNER
	owner := p.GetKind() == typev1.PrincipalKind_PRINCIPAL_KIND_USER &&
		p.GetId().GetValue() == "local-owner" && p.GetName() == "owner" &&
		p.GetRole() == typev1.Role_ROLE_OWNER && p.GetOnBehalfOf() == nil && p.GetDevice() == nil &&
		ctx.Value(localOwnerAdmissionKey{}) == true
	if !local && !owner {
		return nil, apiError(connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_SCOPE_MISSING,
			"this call needs the "+scope+" scope", map[string]string{"scope": scope}, nil)
	}
	return p, nil
}

// localName is an OS user name that fits a PrincipalId after "local:"
// (ids.proto: 1 to 128 characters of [A-Za-z0-9._:/@-]).
var localName = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,122}$`)

// LocalUser is Principal{LOCAL_USER, "local:<os user>"} for a kernel user
// id (02f). lookup resolves the id to a user (nil: os/user.LookupId); when
// it fails, as it can without cgo, or the name does not fit a PrincipalId,
// the name is "uid-<n>".
func LocalUser(uid uint32, lookup func(string) (*user.User, error)) *typev1.Principal {
	if lookup == nil {
		lookup = user.LookupId
	}
	id := strconv.FormatUint(uint64(uid), 10)
	name := "uid-" + id
	if u, err := lookup(id); err == nil && localName.MatchString(u.Username) {
		name = u.Username
	}
	return &typev1.Principal{
		Name: name, Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
		Id: &typev1.PrincipalId{Value: "local:" + name},
	}
}

// authorizeAdmin admits ADMIN (02d) for the kernel-checked admin user or
// the fixed owner admitted by the local TCP listener.
func authorizeAdmin(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "ADMIN")
}

// authorizeSettings admits a caller that holds SETTINGS (02d: settings,
// accounts, repos, routines, integrations' watched projects, machine labels,
// hook installs, config import). The kernel-checked admin user and the
// fixed owner admitted by local TCP both cover it.
func authorizeSettings(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "SETTINGS")
}

// authorizeRead admits a caller that holds READ (02d: config export,
// audit reads, watches). The kernel-checked admin user and the fixed
// owner admitted by local TCP both cover it.
func authorizeRead(ctx context.Context) (*typev1.Principal, error) {
	return authorizeScope(ctx, "READ")
}

// apiError is a Connect error with the 02b ErrorDetail.
func apiError(code connect.Code, reason typev1.ReasonCode, msg string, metadata map[string]string,
	violations []*typev1.FieldViolation,
) *connect.Error {
	err := connect.NewError(code, errors.New(msg))
	d := &typev1.ErrorDetail{Reason: reason, Metadata: metadata, Violations: violations}
	if detail, derr := connect.NewErrorDetail(d); derr == nil {
		err.AddDetail(detail)
	}
	return err
}
