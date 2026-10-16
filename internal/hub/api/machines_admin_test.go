package api_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/ca"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/telemetry"
)

// fakeRevoker is the test revocation list: the in-memory set every
// handshake reads, with Stage recorded. onStage, when set, runs before
// each Stage records: a test observes or delays the exact moment the
// handler stages (a cancel, or a serial that lands in the gap).
type fakeRevoker struct {
	mu      sync.Mutex
	set     map[string]time.Time
	staged  [][]revocation.Entry
	onStage func()
}

func (f *fakeRevoker) Revoked(serial string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.set[serial]
	return ok
}

func (f *fakeRevoker) Stage(add []revocation.Entry) error {
	if f.onStage != nil {
		f.onStage()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staged = append(f.staged, add)
	if f.set == nil {
		f.set = map[string]time.Time{}
	}
	for _, e := range add {
		f.set[e.Serial] = e.NotAfter
	}
	return nil
}

// testMachineAdmin carries the fakes behind one machine admin test.
type testMachineAdmin struct {
	mu      sync.Mutex
	revoker *fakeRevoker
	byes    []string
	closed  [][]outbox.Closed
}

func (a *testMachineAdmin) bye(machineID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byes = append(a.byes, machineID)
	return true
}

func (a *testMachineAdmin) onClosed(_ context.Context, list []outbox.Closed) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = append(a.closed, list)
}

// byeCount reads the recorded stream cuts under the lock, for tests
// that poll while the handler still runs.
func (a *testMachineAdmin) byeCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.byes)
}

// newMachineEnv is newFleetEnv with the machine admin wiring: the fake
// revocation list, the Bye recorder and the closed-commands recorder.
// It lives here (not in the shared helpers) so this slice stays out of
// the files the unmerged row-12 slices edit.
func newMachineEnv(t *testing.T, who *typev1.Principal, adm *testMachineAdmin) *fleetEnv {
	t.Helper()
	auth, err := ca.New(fleetNow, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e := &fleetEnv{dir: filepath.Join(t.TempDir(), "state"), caDER: auth.Cert.Raw, logs: &syncBuffer{}}
	now := func() time.Time { return fleetNow }
	if err := statedb.EnsureDir(e.dir); err != nil {
		t.Fatal(err)
	}
	id := statedb.Identity{
		FleetID: "fleet", CACert: auth.Cert.Raw, CAKey: []byte("k"), ServerCert: []byte("c"), ServerKey: []byte("k"),
		AccountSalt: make([]byte, 32), SaltVersion: 1, SettingsEpoch: "e", CreatedTime: fleetNow,
	}
	if err := statedb.Create(context.Background(), e.dir, id, statedb.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	e.tel, err = telemetry.New(telemetry.Config{Service: telemetry.Hub, Version: "test", InstanceID: "test", LogOutput: e.logs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.tel.Shutdown(context.Background()) })
	e.st, err = statedb.Open(context.Background(), e.dir, e.tel)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.st.Close() })
	local := false
	fleet, err := api.NewFleet(api.FleetOptions{
		Store: e.st, CACert: e.caDER, Telemetry: e.tel, Now: now,
		StateDir: e.dir, LocalTrusted: local,
		Revoker: adm.revoker, ByeMachine: adm.bye, OnCommandsClosed: adm.onClosed,
		HubInstanceID: testInstanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	path, h := fleet.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if who != nil {
			r = r.WithContext(api.WithPrincipal(r.Context(), who))
		}
		h.ServeHTTP(w, r)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.client = hubv1connect.NewFleetServiceClient(srv.Client(), srv.URL)
	return e
}

// seedMachine enrolls one machine straight into the test store (the
// agent wire lives in the gateway package).
func seedMachine(t *testing.T, e *fleetEnv, id string) {
	t.Helper()
	secret := sha256.Sum256([]byte("seed-" + id))
	tok := statedb.EnrollToken{
		SecretHash: secret, MachineName: "laptop", ExpectedHostname: "laptop.lan", Labels: []string{"gpu"},
		NotAfter: fleetNow.Add(time.Hour), Created: fleetNow,
		Principal: "local:alice", RequestID: "seed-" + id, RequestHash: sha256.Sum256([]byte("seed")),
	}
	if err := statedb.InsertEnrollToken(context.Background(), e.st, tok); err != nil {
		t.Fatal(err)
	}
	key := statedb.EnrollKey{SecretHash: secret, InstallID: "install-1", CSRKeyHash: sha256.Sum256([]byte("csr"))}
	if _, err := statedb.Enroll(context.Background(), e.st, statedb.NewMachine{
		Key: key, MachineID: id, CertDER: []byte("cert"), Serial: "ab" + id[len(id)-4:],
		CertNotAfter: fleetNow.Add(90 * 24 * time.Hour), ReportedHostname: "laptop.lan",
		Facts: `{"hostname":"laptop.lan"}`, Now: fleetNow,
	}); err != nil {
		t.Fatal(err)
	}
}

func updateMachineRequest(id, requestID, name string, labels []string, version uint64, paths []string) *hubv1.UpdateMachineRequest {
	return &hubv1.UpdateMachineRequest{
		RequestId: requestID,
		Machine: &hubv1.Machine{
			Id:      &typev1.MachineId{Value: id},
			Spec:    &hubv1.MachineSpec{DisplayName: name, Labels: labels},
			Version: version,
		},
		UpdateMask:      fieldMask(paths),
		ExpectedVersion: version,
	}
}

func TestUpdateMachineRenamesAndLabels(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	res, err := e.client.UpdateMachine(context.Background(), connect.NewRequest(
		updateMachineRequest(id, "req-1", "renamed", []string{"desk"}, 1,
			[]string{"spec.display_name", "spec.labels"})))
	if err != nil {
		t.Fatalf("UpdateMachine: %v", err)
	}
	got := res.Msg.GetMachine()
	if got.GetVersion() != 2 || got.GetSpec().GetDisplayName() != "renamed" {
		t.Errorf("machine = v%d %q, want v2 renamed", got.GetVersion(), got.GetSpec().GetDisplayName())
	}
	if labels := got.GetSpec().GetLabels(); len(labels) != 1 || labels[0] != "desk" {
		t.Errorf("labels = %q, want [desk]", labels)
	}

	replay, err := e.client.UpdateMachine(context.Background(), connect.NewRequest(
		updateMachineRequest(id, "req-1", "renamed", []string{"desk"}, 1,
			[]string{"spec.display_name", "spec.labels"})))
	if err != nil {
		t.Fatalf("UpdateMachine replay: %v", err)
	}
	if replay.Msg.GetMachine().GetVersion() != 2 {
		t.Errorf("replay version = %d, want 2", replay.Msg.GetMachine().GetVersion())
	}

	_, err = e.client.UpdateMachine(context.Background(), connect.NewRequest(
		updateMachineRequest(id, "req-3", "stale", nil, 1, []string{"spec.display_name"})))
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH {
		t.Errorf("stale = %v %v, want FAILED_PRECONDITION VERSION_MISMATCH", code, reason)
	}

	_, err = e.client.UpdateMachine(context.Background(), connect.NewRequest(
		updateMachineRequest("01jmissing0000000000000001", "req-4", "x", nil, 0, []string{"spec.display_name"})))
	code, reason, _ = detail(t, err)
	if code != connect.CodeNotFound || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("missing = %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, reason)
	}
}

func TestUpdateMachineRefusesABadMask(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	_, err := e.client.UpdateMachine(context.Background(), connect.NewRequest(
		updateMachineRequest(id, "req-1", "renamed", nil, 0, []string{"spec.drain"})))
	code, reason, fields := detail(t, err)
	if code != connect.CodeInvalidArgument || reason != typev1.ReasonCode_REASON_CODE_UPDATE_MASK_INVALID {
		t.Errorf("drain mask = %v %v %v, want INVALID_ARGUMENT UPDATE_MASK_INVALID", code, reason, fields)
	}

	_, err = e.client.UpdateMachine(context.Background(), connect.NewRequest(
		updateMachineRequest(id, "req-2", "renamed", nil, 0, nil)))
	code, reason, _ = detail(t, err)
	if code != connect.CodeInvalidArgument || reason != typev1.ReasonCode_REASON_CODE_UPDATE_MASK_INVALID {
		t.Errorf("empty mask = %v %v, want INVALID_ARGUMENT UPDATE_MASK_INVALID", code, reason)
	}
}

func TestRevokeMachineRevokesAndByes(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	res, err := e.client.RevokeMachine(context.Background(), connect.NewRequest(&hubv1.RevokeMachineRequest{
		RequestId: "req-1", Id: &typev1.MachineId{Value: id}, Reason: "stolen laptop",
	}))
	if err != nil {
		t.Fatalf("RevokeMachine: %v", err)
	}
	got := res.Msg.GetMachine()
	if got.GetVersion() != 2 {
		t.Errorf("version = %d, want 2", got.GetVersion())
	}
	if got.GetStatus().GetState() != hubv1.MachineState_MACHINE_STATE_REVOKED {
		t.Errorf("state = %v, want REVOKED", got.GetStatus().GetState())
	}
	rev := got.GetSpec().GetRevocation()
	if rev.GetReason() != "stolen laptop" || rev.GetBy().GetId().GetValue() != "local:alice" {
		t.Errorf("revocation = %+v, want the reason and the caller", rev)
	}
	if len(adm.revoker.staged) != 1 || len(adm.revoker.staged[0]) != 1 ||
		adm.revoker.staged[0][0].Serial != "ab0001" {
		t.Errorf("staged = %+v, want [ab0001]", adm.revoker.staged)
	}
	if len(adm.byes) != 1 || adm.byes[0] != id {
		t.Errorf("byes = %q, want [%q]", adm.byes, id)
	}

	again, err := e.client.RevokeMachine(context.Background(), connect.NewRequest(&hubv1.RevokeMachineRequest{
		RequestId: "req-2", Id: &typev1.MachineId{Value: id}, Reason: "stolen laptop",
	}))
	if err != nil {
		t.Fatalf("second RevokeMachine: %v", err)
	}
	if again.Msg.GetMachine().GetVersion() != 2 {
		t.Errorf("second version = %d, want 2 (idempotent)", again.Msg.GetMachine().GetVersion())
	}
	if len(adm.revoker.staged) != 1 {
		t.Errorf("staged calls = %d, want 1 (already on the list)", len(adm.revoker.staged))
	}

	_, err = e.client.RevokeMachine(context.Background(), connect.NewRequest(&hubv1.RevokeMachineRequest{
		RequestId: "req-3", Id: &typev1.MachineId{Value: "01jmissing0000000000000001"},
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeNotFound || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("missing = %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, reason)
	}
}

func TestDeleteMachineNeedsARevokedMachine(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	_, err := e.client.DeleteMachine(context.Background(), connect.NewRequest(&hubv1.DeleteMachineRequest{
		RequestId: "req-1", Id: &typev1.MachineId{Value: id},
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_MACHINE_NOT_REVOKED {
		t.Fatalf("live delete = %v %v, want FAILED_PRECONDITION MACHINE_NOT_REVOKED", code, reason)
	}

	if _, err := e.client.RevokeMachine(context.Background(), connect.NewRequest(&hubv1.RevokeMachineRequest{
		RequestId: "req-2", Id: &typev1.MachineId{Value: id},
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.DeleteMachine(context.Background(), connect.NewRequest(&hubv1.DeleteMachineRequest{
		RequestId: "req-3", Id: &typev1.MachineId{Value: id}, ExpectedVersion: 2,
	})); err != nil {
		t.Fatalf("DeleteMachine: %v", err)
	}
	if _, err := e.client.DeleteMachine(context.Background(), connect.NewRequest(&hubv1.DeleteMachineRequest{
		RequestId: "req-3", Id: &typev1.MachineId{Value: id}, ExpectedVersion: 2,
	})); err != nil {
		t.Fatalf("DeleteMachine replay: %v", err)
	}

	_, err = e.client.DeleteMachine(context.Background(), connect.NewRequest(&hubv1.DeleteMachineRequest{
		RequestId: "req-4", Id: &typev1.MachineId{Value: id},
	}))
	code, reason, _ = detail(t, err)
	if code != connect.CodeNotFound || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("after delete = %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, reason)
	}
}

func TestMachineWritesAreCounted(t *testing.T) {
	t.Parallel()
	adm := &testMachineAdmin{revoker: &fakeRevoker{}}
	e := newMachineEnv(t, localUser("alice"), adm)
	id := "01jmachine0000000000000001"
	seedMachine(t, e, id)

	if _, err := e.client.UpdateMachine(context.Background(), connect.NewRequest(
		updateMachineRequest(id, "req-1", "renamed", nil, 0, []string{"spec.display_name"}))); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.RevokeMachine(context.Background(), connect.NewRequest(&hubv1.RevokeMachineRequest{
		RequestId: "req-2", Id: &typev1.MachineId{Value: id},
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.DeleteMachine(context.Background(), connect.NewRequest(&hubv1.DeleteMachineRequest{
		RequestId: "req-3", Id: &typev1.MachineId{Value: id},
	})); err != nil {
		t.Fatal(err)
	}
	body := e.scrape(t)
	for _, op := range []string{"machine_update", "machine_revoke", "machine_delete"} {
		if got := configSample(t, body, op, "ok"); got != 1 {
			t.Errorf("config_writes_total{%s ok} = %v, want 1", op, got)
		}
	}
	events := e.logs.events(t, "config.machine_written")
	if len(events) != 3 {
		t.Errorf("config.machine_written events = %d, want 3", len(events))
	}
}
