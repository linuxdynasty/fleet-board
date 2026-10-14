package api_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func enrollNamedMachine(t *testing.T, e *fleetEnv, body byte, name, id string, drained bool) {
	t.Helper()
	at := fleetNow.Add(time.Duration(body) * time.Minute)
	secret := sha256.Sum256([]byte{body, 1, 2, 3})
	tok := statedb.EnrollToken{
		SecretHash: secret, MachineName: name, ExpectedHostname: name + ".lan",
		Labels: []string{"gpu"}, StartDrained: drained,
		NotAfter: at.Add(24 * time.Hour), Created: at,
		Principal: "local:alice", RequestID: "req-" + id,
		RequestHash: sha256.Sum256([]byte{body}),
	}
	if err := statedb.InsertEnrollToken(context.Background(), e.st, tok); err != nil {
		t.Fatalf("InsertEnrollToken: %v", err)
	}
	facts := fmt.Sprintf(`{"hostname":%q,"os":"linux","os_version":"15.0","arch":"arm64",`+
		`"cpu_count":8,"ram_bytes":17179869184,"timezone":"America/New_York",`+
		`"agent_version":"0.1","tmux_version":"3.8","username":"ada","container":false}`,
		name+".lan")
	if _, err := statedb.Enroll(context.Background(), e.st, statedb.NewMachine{
		Key: statedb.EnrollKey{
			SecretHash: secret, InstallID: "install-" + id,
			CSRKeyHash: sha256.Sum256([]byte{body, 4}),
		},
		MachineID: id, CertDER: []byte("cert-" + id), Serial: fmt.Sprintf("aa%02x", body),
		CertNotAfter: at.Add(90 * 24 * time.Hour), ReportedHostname: name + ".lan",
		Facts: facts, Now: at,
	}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
}

func listMachines(t *testing.T, e *fleetEnv, req *hubv1.ListMachinesRequest) *hubv1.ListMachinesResponse {
	t.Helper()
	resp, err := e.client.ListMachines(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	return resp.Msg
}

// errorOffset reads the 02c byte offset of the bad filter atom.
func errorOffset(t *testing.T, err error) string {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error %v is not a *connect.Error", err)
	}
	for _, d := range ce.Details() {
		msg, derr := d.Value()
		if derr != nil {
			t.Fatal(derr)
		}
		if ed, ok := msg.(*typev1.ErrorDetail); ok {
			return ed.GetMetadata()["offset"]
		}
	}
	return ""
}
