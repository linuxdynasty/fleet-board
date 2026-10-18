package daemon_test

import (
	"context"
	"io"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/daemon"
)

func TestLocalAPIUsesTheRunningFleetsIdentity(t *testing.T) {
	h, r := startLocalAPI(t, "")
	client := hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+r.addrs.API.String())
	got, err := client.GetServerInfo(t.Context(), connect.NewRequest(&hubv1.GetServerInfoRequest{}))
	if err != nil {
		t.Fatalf("TCP GetServerInfo: %v", err)
	}
	fromSocket, err := api.AdminClient(config.AdminSocketPath(h.state)).GetServerInfo(context.Background(),
		connect.NewRequest(&hubv1.GetServerInfoRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.GetHubInstanceId() != fromSocket.Msg.GetHubInstanceId() || got.Msg.GetFleetId() != fromSocket.Msg.GetFleetId() {
		t.Errorf("TCP identity differs from admin socket: TCP %v, socket %v", got.Msg, fromSocket.Msg)
	}
	caller := got.Msg.GetCaller()
	if caller.GetId().GetValue() != "local-owner" || caller.GetName() != "owner" ||
		caller.GetKind() != typev1.PrincipalKind_PRINCIPAL_KIND_USER || caller.GetRole() != typev1.Role_ROLE_OWNER {
		t.Errorf("TCP caller = %v, want fixed local owner", caller)
	}
	wantScopes := []hubv1.Scope{
		hubv1.Scope_SCOPE_READ, hubv1.Scope_SCOPE_SESSIONS_WRITE, hubv1.Scope_SCOPE_APPROVALS,
		hubv1.Scope_SCOPE_SETTINGS, hubv1.Scope_SCOPE_ADMIN, hubv1.Scope_SCOPE_LOGS,
	}
	if gotScopes := got.Msg.GetCallerScopes(); len(gotScopes) != len(wantScopes) {
		t.Errorf("TCP scopes = %v, want %v", gotScopes, wantScopes)
	} else {
		for i, scope := range wantScopes {
			if gotScopes[i] != scope {
				t.Errorf("TCP scopes = %v, want %v", gotScopes, wantScopes)
				break
			}
		}
	}
}

func startLocalAPI(t *testing.T, extra string) (*hub, *running) {
	t.Helper()
	h := newHub(t, extra)
	opts := h.options()
	opts.SecretOutput = io.Discard
	if err := daemon.Init(t.Context(), opts); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return h, h.start(t, opts)
}
