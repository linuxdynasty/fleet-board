package api_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// deviceCaller holds no SETTINGS or ADMIN scope: only the local user on
// the admin socket does until plan row 12 brings caller scopes.
func deviceCaller() *typev1.Principal {
	return &typev1.Principal{
		Name: "agent", Role: typev1.Role_ROLE_OWNER,
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE,
		Id:   &typev1.PrincipalId{Value: "device:a"},
	}
}

func TestMachineAdminWritesNeedACaller(t *testing.T) {
	t.Parallel()
	id := "01jmachine0000000000000001"
	calls := map[string]func(e *fleetEnv) error{
		"UpdateMachine": func(e *fleetEnv) error {
			_, err := e.client.UpdateMachine(context.Background(), connect.NewRequest(
				updateMachineRequest(id, "req-1", "renamed", nil, 0, []string{"spec.display_name"})))
			return err
		},
		"RevokeMachine": func(e *fleetEnv) error {
			_, err := e.client.RevokeMachine(context.Background(), connect.NewRequest(&hubv1.RevokeMachineRequest{
				RequestId: "req-1", Id: &typev1.MachineId{Value: id},
			}))
			return err
		},
		"DeleteMachine": func(e *fleetEnv) error {
			_, err := e.client.DeleteMachine(context.Background(), connect.NewRequest(&hubv1.DeleteMachineRequest{
				RequestId: "req-1", Id: &typev1.MachineId{Value: id},
			}))
			return err
		},
	}
	for rpc, call := range calls {
		t.Run(rpc+" without a principal is refused", func(t *testing.T) {
			t.Parallel()
			adm := &testMachineAdmin{revoker: &fakeRevoker{}}
			e := newMachineEnv(t, nil, adm)
			code, reason, _ := detail(t, call(e))
			if code != connect.CodeUnauthenticated ||
				reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
				t.Errorf("= %v/%v, want UNAUTHENTICATED/CREDENTIALS_INVALID", code, reason)
			}
		})
		t.Run(rpc+" without the scope is refused", func(t *testing.T) {
			t.Parallel()
			adm := &testMachineAdmin{revoker: &fakeRevoker{}}
			e := newMachineEnv(t, deviceCaller(), adm)
			code, reason, _ := detail(t, call(e))
			if code != connect.CodePermissionDenied ||
				reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
				t.Errorf("= %v/%v, want PERMISSION_DENIED/SCOPE_MISSING", code, reason)
			}
		})
	}
}
