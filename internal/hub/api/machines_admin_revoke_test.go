package api_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func revokeRequest(requestID, id string, version uint64, reason string) *hubv1.RevokeMachineRequest {
	return &hubv1.RevokeMachineRequest{
		RequestId: requestID, Id: &typev1.MachineId{Value: id},
		ExpectedVersion: version, Reason: reason,
	}
}

func TestRevokeMachineStaleVersionStagesNothing(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	_, err := e.client.RevokeMachine(context.Background(), connect.NewRequest(revokeRequest("req-1", id, 9, "")))
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH {
		t.Fatalf("stale = %v %v, want FAILED_PRECONDITION VERSION_MISMATCH", code, reason)
	}
	if len(adm.revoker.staged) != 0 {
		t.Errorf("staged = %+v, want nothing staged for a refused revoke", adm.revoker.staged)
	}
	if len(adm.byes) != 0 {
		t.Errorf("byes = %q, want no stream cut for a refused revoke", adm.byes)
	}
}

func TestMachineAdminReplayNeedsTheKind(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	// An empty reason keeps wire fields 1-3 only, so this revoke and a
	// delete under the same request id hash identically.
	if _, err := e.client.RevokeMachine(context.Background(),
		connect.NewRequest(revokeRequest("req-1", id, 0, ""))); err != nil {
		t.Fatal(err)
	}
	_, err := e.client.DeleteMachine(context.Background(), connect.NewRequest(&hubv1.DeleteMachineRequest{
		RequestId: "req-1", Id: &typev1.MachineId{Value: id},
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeAlreadyExists || reason != typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT {
		t.Fatalf("cross-kind replay = %v %v, want ALREADY_EXISTS REQUEST_ID_CONFLICT", code, reason)
	}
	// The machine survives: the delete never ran.
	if _, err := e.client.DeleteMachine(context.Background(), connect.NewRequest(&hubv1.DeleteMachineRequest{
		RequestId: "req-2", Id: &typev1.MachineId{Value: id},
	})); err != nil {
		t.Fatalf("DeleteMachine after the refused replay: %v", err)
	}
}

// reenrollMachine re-enrolls a revoked machine through the production
// path with a fresh serial, as the agent wire would.
func reenrollMachine(t *testing.T, e *fleetEnv, id, serial string) {
	t.Helper()
	secret := sha256.Sum256([]byte("reenroll-" + id + "-" + serial))
	tok := statedb.EnrollToken{
		SecretHash: secret, MachineName: "laptop", ExpectedHostname: "laptop.lan",
		NotAfter: fleetNow.Add(time.Hour), Created: fleetNow,
		Principal: "local:alice", RequestID: "tok-" + serial, RequestHash: sha256.Sum256([]byte("tok")),
		PreviousMachine: id,
	}
	if err := statedb.InsertEnrollToken(context.Background(), e.st, tok); err != nil {
		t.Fatal(err)
	}
	key := statedb.EnrollKey{SecretHash: secret, InstallID: "install-2", CSRKeyHash: sha256.Sum256([]byte("csr"))}
	if _, err := statedb.EnrollPrevious(context.Background(), e.st, statedb.ReenrollMachine{
		Key: key, PreviousMachine: id, CertDER: []byte("cert-new-" + serial), Serial: serial,
		CertNotAfter: fleetNow.Add(90 * 24 * time.Hour), ReportedHostname: "laptop.lan",
		Facts: `{"hostname":"laptop.lan"}`, Now: fleetNow,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeMachineReplayAfterReenrollLeavesTheNewStreamAlone(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	if _, err := e.client.RevokeMachine(context.Background(),
		connect.NewRequest(revokeRequest("req-1", id, 0, "stolen"))); err != nil {
		t.Fatal(err)
	}
	reenrollMachine(t, e, id, "bb0002")

	again, err := e.client.RevokeMachine(context.Background(),
		connect.NewRequest(revokeRequest("req-1", id, 0, "stolen")))
	if err != nil {
		t.Fatalf("replayed RevokeMachine: %v", err)
	}
	got := again.Msg.GetMachine()
	if got.GetVersion() != 2 || got.GetStatus().GetState() != hubv1.MachineState_MACHINE_STATE_REVOKED {
		t.Errorf("replay = v%d %v, want the stored v2 REVOKED response", got.GetVersion(), got.GetStatus().GetState())
	}
	if len(adm.revoker.staged) != 1 {
		t.Errorf("staged calls = %d, want 1 (the new serial is not revoked)", len(adm.revoker.staged))
	}
	if adm.revoker.Revoked("bb0002") {
		t.Error("bb0002 is on the revocation set, want the re-enrolled serial live")
	}
	if len(adm.byes) != 1 {
		t.Errorf("byes = %q, want only the first revoke's cut", adm.byes)
	}
}

// waitForBye polls for the stream cut until the deadline: the handler
// of a canceled request finishes on its own detached context.
func waitForBye(t *testing.T, adm *testMachineAdmin) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for adm.byeCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no Bye within 10 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRevokeMachineCanceledRequestStillCutsTheStream(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	// The client's disconnect lands while the handler stages, before
	// the store write. The commit and its after-commit work are owned
	// detached from the request, so the stream is still cut.
	ctx, cancel := context.WithCancel(context.Background())
	adm.revoker.onStage = cancel
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.client.RevokeMachine(ctx, connect.NewRequest(revokeRequest("req-1", id, 0, "")))
	}()
	waitForBye(t, adm)
	<-done
	if !adm.revoker.Revoked("ab0001") {
		t.Error("ab0001 is not on the revocation set after a canceled revoke")
	}
	if n := adm.byeCount(); n != 1 {
		t.Errorf("byes = %d, want 1 cut stream after a canceled revoke", n)
	}
}

func TestRevokeMachineStagesARenewalFromTheGap(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	// A RenewCert insert commits while the handler is inside Stage,
	// after its serial read: the writer revokes the new serial in
	// state.db, and the post-commit reconcile stages it, so no valid
	// certificate survives the revoke.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	adm.revoker.onStage = func() {
		once.Do(func() { close(entered) })
		<-release
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.client.RevokeMachine(context.Background(),
			connect.NewRequest(revokeRequest("req-1", id, 0, "")))
		done <- err
	}()
	<-entered
	// The renewal's rows, as statedb Renew would write them.
	nowMs := fleetNow.UnixMilli()
	if err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `INSERT INTO cert_serials(serial, machine_id,
			not_after_time, created_time) VALUES (?, ?, ?, ?)`,
			"cd0002", id, fleetNow.Add(90*24*time.Hour).UnixMilli(), nowMs)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RevokeMachine: %v", err)
	}
	if !adm.revoker.Revoked("ab0001") || !adm.revoker.Revoked("cd0002") {
		t.Errorf("revoked = ab0001:%v cd0002:%v, want both staged",
			adm.revoker.Revoked("ab0001"), adm.revoker.Revoked("cd0002"))
	}
}
