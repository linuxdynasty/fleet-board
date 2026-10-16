package api_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/buildinfo"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
)

// newFleet rebuilds the env's Fleet service with a chosen instance id, to
// prove the constructor guards the wiring.
func newFleet(e *fleetEnv, instanceID string) (*api.Fleet, error) {
	return api.NewFleet(api.FleetOptions{
		Store: e.st, CACert: e.caDER, Telemetry: e.tel,
		StateDir: e.dir, LocalTrusted: true, HubInstanceID: instanceID,
	})
}

// testInstanceID is the hub_instance_id every fleetEnv reports: it stands in
// for the gateway's per-process id the daemon passes to the Fleet service.
const testInstanceID = "test-instance-id"

// TestGetServerInfoServesIdentityAndVersion is 02k's handshake read: who the
// hub is, what it serves, and who the caller is, with no scope gate.
func TestGetServerInfoServesIdentityAndVersion(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	resp, err := e.client.GetServerInfo(context.Background(), connect.NewRequest(&hubv1.GetServerInfoRequest{}))
	if err != nil {
		t.Fatalf("GetServerInfo: %v", err)
	}
	msg := resp.Msg
	if msg.GetApiVersion() != 1 {
		t.Errorf("api_version %d, want 1", msg.GetApiVersion())
	}
	if msg.GetHubVersion() != buildinfo.Version {
		t.Errorf("hub_version %q, want buildinfo.Version %q", msg.GetHubVersion(), buildinfo.Version)
	}
	if msg.GetFleetId() != "fleet" {
		t.Errorf("fleet_id %q, want the hub_identity row %q", msg.GetFleetId(), "fleet")
	}
	if msg.GetHubInstanceId() != testInstanceID {
		t.Errorf("hub_instance_id %q, want %q", msg.GetHubInstanceId(), testInstanceID)
	}
	if got := msg.GetServerTime().AsTime(); !got.Equal(fleetNow) {
		t.Errorf("server_time %v, want the hub clock %v", got, fleetNow)
	}
	if msg.GetAuthMode() != hubv1.AuthMode_AUTH_MODE_LOCAL_TRUSTED {
		t.Errorf("auth_mode %s, want LOCAL_TRUSTED", msg.GetAuthMode())
	}
	if got := msg.GetCaller().GetId().GetValue(); got != "local:alice" {
		t.Errorf("caller %q, want the caller as the hub sees it", got)
	}
	wantScopes := map[hubv1.Scope]bool{
		hubv1.Scope_SCOPE_READ: true, hubv1.Scope_SCOPE_SESSIONS_WRITE: true,
		hubv1.Scope_SCOPE_APPROVALS: true, hubv1.Scope_SCOPE_SETTINGS: true,
		hubv1.Scope_SCOPE_ADMIN: true, hubv1.Scope_SCOPE_LOGS: true,
	}
	if len(msg.GetCallerScopes()) != len(wantScopes) {
		t.Fatalf("caller_scopes %v, want the OWNER set", msg.GetCallerScopes())
	}
	for _, s := range msg.GetCallerScopes() {
		if !wantScopes[s] {
			t.Errorf("caller_scopes holds %s, outside the OWNER set", s)
		}
	}
	// 02k: features lists exactly what is served. No optional feature
	// (attach, routines, devices, watch.pane_summaries) is built yet.
	for _, f := range msg.GetFeatures() {
		t.Errorf("feature %q advertised, but nothing optional is served yet", f.GetName())
	}
	if len(msg.GetSignInMethods()) != 0 {
		t.Errorf("sign_in_methods %v, want none in local_trusted", msg.GetSignInMethods())
	}
	if msg.GetClaimPending() {
		t.Error("claim_pending, want false in local_trusted")
	}
}

// TestGetServerInfoNeedsNoScope proves the handshake read stays open to a
// caller with no usable scope and to no caller at all (common.proto:
// "Allowed without scopes").
func TestGetServerInfoNeedsNoScope(t *testing.T) {
	t.Parallel()
	viewer := &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_VIEWER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_LOCAL_USER,
		Id: &typev1.PrincipalId{Value: "local:bob"},
	}
	e := newFleetEnv(t, viewer)
	resp, err := e.client.GetServerInfo(context.Background(), connect.NewRequest(&hubv1.GetServerInfoRequest{}))
	if err != nil {
		t.Fatalf("GetServerInfo as VIEWER: %v", err)
	}
	if len(resp.Msg.GetCallerScopes()) != 1 || resp.Msg.GetCallerScopes()[0] != hubv1.Scope_SCOPE_READ {
		t.Errorf("caller_scopes %v, want the VIEWER set [READ]", resp.Msg.GetCallerScopes())
	}

	anon := newFleetEnv(t, nil)
	ar, err := anon.client.GetServerInfo(context.Background(), connect.NewRequest(&hubv1.GetServerInfoRequest{}))
	if err != nil {
		t.Fatalf("GetServerInfo with no caller: %v", err)
	}
	if ar.Msg.GetCaller().GetId().GetValue() != "" {
		t.Errorf("caller %v, want unset when not signed in", ar.Msg.GetCaller())
	}
	if len(ar.Msg.GetCallerScopes()) != 0 {
		t.Errorf("caller_scopes %v, want none when not signed in", ar.Msg.GetCallerScopes())
	}
}

// TestNewFleetRefusesMissingInstanceID pins the wiring shut: a Fleet
// service without the gateway's hub_instance_id would report an id no
// agent ever saw, so the constructor fails instead.
func TestNewFleetRefusesMissingInstanceID(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	if _, err := newFleet(e, ""); err == nil {
		t.Fatal("NewFleet without HubInstanceID succeeded, want an error")
	}
}

// TestUnbuiltRPCsStayUnimplemented pins 02k's out-of-release rule: a v3 RPC
// and a v1 RPC whose row has not landed answer UNIMPLEMENTED, not a
// connection error.
func TestUnbuiltRPCsStayUnimplemented(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	_, err := e.client.RedeemInvite(context.Background(), connect.NewRequest(&hubv1.RedeemInviteRequest{}))
	if code, _, _ := detail(t, err); code != connect.CodeUnimplemented {
		t.Errorf("RedeemInvite code %s, want UNIMPLEMENTED (v3, no row builds it)", code)
	}
	_, err = e.client.DrainMachine(context.Background(), connect.NewRequest(&hubv1.DrainMachineRequest{}))
	if code, _, _ := detail(t, err); code != connect.CodeUnimplemented {
		t.Errorf("DrainMachine code %s, want UNIMPLEMENTED (row 24, not landed)", code)
	}
}
