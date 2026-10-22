package api_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/outbox"
	"github.com/linuxdynasty/fleet-board/internal/hub/revocation"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

type countingRevoker struct {
	list       *revocation.List
	stages     atomic.Int32
	onStage    func()
	afterStage func()
}

func (r *countingRevoker) Revoked(serial string) bool { return r.list.Revoked(serial) }
func (r *countingRevoker) Stage(add []revocation.Entry) error {
	r.stages.Add(1)
	if r.onStage != nil {
		r.onStage()
	}
	err := r.list.Stage(add)
	if r.afterStage != nil {
		r.afterStage()
	}
	return err
}

type tcpRevokeEnv struct {
	*fleetEnv
	client      hubv1connect.FleetServiceClient
	secret      string
	revoker     *countingRevoker
	admin       *testMachineAdmin
	clock       atomic.Int64
	afterClosed func()
}

func newTCPRevokeEnv(t *testing.T, writeLimit int) *tcpRevokeEnv {
	t.Helper()
	admin := &testMachineAdmin{revoker: &fakeRevoker{}}
	base := newMachineEnv(t, localUser("alice"), admin)
	if _, err := secret.Ensure(base.dir, nil); err != nil {
		t.Fatal(err)
	}
	presented, err := secret.Reveal(base.dir) // only this fresh test folder
	if err != nil {
		t.Fatal(err)
	}
	list, err := revocation.Load(t.Context(), revocation.Options{
		Dir: base.dir, Store: base.st,
		Now: func() time.Time { return fleetNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	e := &tcpRevokeEnv{
		fleetEnv: base, secret: presented, admin: admin,
		revoker: &countingRevoker{list: list},
	}
	e.clock.Store(fleetNow.UnixNano())
	q, err := api.NewQuotas(config.RateLimits{Read: 100, Write: writeLimit, Unauthenticated: 100, Metrics: 100},
		base.tel, func() time.Time { return time.Unix(0, e.clock.Load()) })
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := api.NewFleet(api.FleetOptions{
		Store: base.st, CACert: base.caDER, Telemetry: base.tel, Now: func() time.Time { return fleetNow },
		StateDir: base.dir, LocalTrusted: true, Revoker: e.revoker, ByeMachine: admin.bye,
		OnCommandsClosed: func(ctx context.Context, closed []outbox.Closed) {
			admin.onClosed(ctx, closed)
			if e.afterClosed != nil {
				e.afterClosed()
			}
		}, HubInstanceID: testInstanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.NewLocalServer(base.tel, port, fleet, q)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	e.client = hubv1connect.NewFleetServiceClient(http.DefaultClient, "http://"+ln.Addr().String())
	return e
}

func (e *tcpRevokeEnv) revoke(ctx context.Context, requestID, id string, version uint64) error {
	req := connect.NewRequest(revokeRequest(requestID, id, version, "stolen"))
	req.Header().Set(api.SecretHeader, e.secret)
	_, err := e.client.RevokeMachine(ctx, req)
	return err
}

func TestTCPRevokeQuotaRefusalHasNoEffect(t *testing.T) {
	e := newTCPRevokeEnv(t, 1)
	first, second := "01jmachine0000000000000001", "01jmachine0000000000000002"
	seedMachine(t, e.fleetEnv, first)
	seedMachine(t, e.fleetEnv, second)
	if err := e.revoke(t.Context(), "first", first, 0); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(e.dir, revocation.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, requestID, machine string
		version                  uint64
		code                     connect.Code
		reason                   typev1.ReasonCode
	}{
		{"stale", "stale", second, 9, connect.CodeFailedPrecondition, typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH},
		{"conflict", "first", second, 0, connect.CodeAlreadyExists, typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT},
		{"unknown", "missing", "01jmissing0000000000000001", 0, connect.CodeNotFound, typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := e.revoke(t.Context(), tc.requestID, tc.machine, tc.version)
			code, reason, _ := detail(t, err)
			if code != tc.code || reason != tc.reason {
				t.Errorf("refusal = %v %v, want %v %v", code, reason, tc.code, tc.reason)
			}
		})
	}
	err = e.revoke(t.Context(), "second", second, 0)
	if err == nil {
		t.Fatal("exhausted revoke succeeded and could stage the second machine")
	}
	code, reason, _ := detail(t, err)
	if code != connect.CodeResourceExhausted || reason != typev1.ReasonCode_REASON_CODE_RATE_LIMITED {
		t.Fatalf("exhausted revoke = %v %v, want RESOURCE_EXHAUSTED RATE_LIMITED", code, reason)
	}
	after, err := os.ReadFile(filepath.Join(e.dir, revocation.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || strings.Contains(string(after), "ab0002") || e.revoker.Revoked("ab0002") {
		t.Error("exhausted revoke changed the revocation file or in-memory set")
	}
	if got := e.revoker.stages.Load(); got != 1 {
		t.Errorf("Stage calls = %d, want 1", got)
	}
	if got := e.admin.byeCount(); got != 1 {
		t.Errorf("Bye calls = %d, want 1", got)
	}
	var state string
	var requests, audits int
	err = e.st.Read(t.Context(), func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(t.Context(), `SELECT state FROM machines WHERE machine_id = ?`, second).Scan(&state); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM machine_request_log WHERE principal_id = 'local-owner' AND request_id = 'second'`).Scan(&requests); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action = 'machine.revoke'`).Scan(&audits)
	})
	if err != nil {
		t.Fatal(err)
	}
	if state != statedb.MachineEnrolled || requests != 0 || audits != 1 {
		t.Errorf("after refused revoke: state=%s request rows=%d revoke audits=%d", state, requests, audits)
	}
}

func TestTCPRevokeLateVersionFailureRefundsWithoutUnrevoking(t *testing.T) {
	e := newTCPRevokeEnv(t, 1)
	first, second := "01jmachine0000000000000001", "01jmachine0000000000000002"
	seedMachine(t, e.fleetEnv, first)
	seedMachine(t, e.fleetEnv, second)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var firstStage atomic.Bool
	e.revoker.onStage = func() {
		if firstStage.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { done <- e.revoke(context.Background(), "late", first, 1) }()
	<-entered // quota is reserved; the serial has not yet reached the file
	_, err := e.fleetEnv.client.UpdateMachine(t.Context(), connect.NewRequest(
		updateMachineRequest(first, "rename", "renamed", nil, 1, []string{"spec.display_name"})))
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	err = <-done
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH {
		t.Fatalf("late refusal = %v %v, want VERSION_MISMATCH", code, reason)
	}
	if !e.revoker.Revoked("ab0001") {
		t.Error("late failure unrevoked the staged serial")
	}
	file, err := os.ReadFile(filepath.Join(e.dir, revocation.FileName))
	if err != nil || !strings.Contains(string(file), "ab0001") {
		t.Errorf("staged serial missing from file: %v", err)
	}
	if err := e.revoke(t.Context(), "second", second, 1); err != nil {
		t.Fatalf("refunded allowance did not admit another revoke: %v", err)
	}
	if got := e.admin.byeCount(); got != 1 {
		t.Errorf("Bye calls = %d, want only the second revoke", got)
	}
}

func TestTCPRevokeLateRequestConflictRefunds(t *testing.T) {
	e := newTCPRevokeEnv(t, 1)
	first, second := "01jmachine0000000000000001", "01jmachine0000000000000002"
	seedMachine(t, e.fleetEnv, first)
	seedMachine(t, e.fleetEnv, second)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var firstStage atomic.Bool
	e.revoker.onStage = func() {
		if firstStage.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { done <- e.revoke(context.Background(), "late", first, 1) }()
	<-entered
	_, err := statedb.UpdateMachine(t.Context(), e.st, statedb.MachineUpdate{
		MachineWrite: statedb.MachineWrite{
			Principal: "local-owner", RequestID: "late", RequestHash: sha256.Sum256([]byte("other body")),
			Now: fleetNow,
		},
		MachineID: first, ExpectedVersion: 1, HasName: true, Name: "renamed",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	err = <-done
	code, reason, _ := detail(t, err)
	if code != connect.CodeAlreadyExists || reason != typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT {
		t.Fatalf("late conflict = %v %v, want REQUEST_ID_CONFLICT", code, reason)
	}
	if !e.revoker.Revoked("ab0001") {
		t.Error("conflict unrevoked the staged serial")
	}
	if err := e.revoke(t.Context(), "second", second, 1); err != nil {
		t.Fatalf("conflict did not refund the allowance: %v", err)
	}
}

func TestTCPRevokeReplayAndNoOpEachUseOneAllowance(t *testing.T) {
	e := newTCPRevokeEnv(t, 3)
	first, second := "01jmachine0000000000000001", "01jmachine0000000000000002"
	seedMachine(t, e.fleetEnv, first)
	seedMachine(t, e.fleetEnv, second)
	for _, requestID := range []string{"first", "first", "noop"} {
		if err := e.revoke(t.Context(), requestID, first, 1); err != nil {
			t.Fatalf("revoke %s: %v", requestID, err)
		}
	}
	if err := e.revoke(t.Context(), "second", second, 1); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("after success, replay and no-op = %v, want quota refusal", err)
	}
	if got := e.revoker.stages.Load(); got != 1 {
		t.Errorf("Stage calls = %d, want one file append", got)
	}
	var audits, logs int
	err := e.st.Read(t.Context(), func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_log WHERE action = 'machine.revoke'`).Scan(&audits); err != nil {
			return err
		}
		return tx.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM machine_request_log WHERE principal_id = 'local-owner' AND kind = 'revoke'`).Scan(&logs)
	})
	if err != nil {
		t.Fatal(err)
	}
	if audits != 1 || logs != 2 {
		t.Errorf("audit=%d request logs=%d, want 1 and 2 (replay adds none)", audits, logs)
	}
}

func TestTCPRevokeReplayAfterReenrollChargesWithoutCuttingNewSerial(t *testing.T) {
	e := newTCPRevokeEnv(t, 2)
	id := "01jmachine0000000000000001"
	seedMachine(t, e.fleetEnv, id)
	if err := e.revoke(t.Context(), "first", id, 1); err != nil {
		t.Fatal(err)
	}
	reenrollMachine(t, e.fleetEnv, id, "bb0002")
	if err := e.revoke(t.Context(), "first", id, 1); err != nil {
		t.Fatalf("replay after re-enroll: %v", err)
	}
	if e.revoker.Revoked("bb0002") || e.admin.byeCount() != 1 || e.revoker.stages.Load() != 1 {
		t.Errorf("replay affected re-enrolled serial: revoked=%v Bye=%d Stage=%d",
			e.revoker.Revoked("bb0002"), e.admin.byeCount(), e.revoker.stages.Load())
	}
	if err := e.revoke(t.Context(), "new", id, 0); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("replay did not spend its allowance: %v", err)
	}
}

func TestTCPConcurrentRevokePermitsDoNotOversubscribe(t *testing.T) {
	e := newTCPRevokeEnv(t, 1)
	first, second := "01jmachine0000000000000001", "01jmachine0000000000000002"
	seedMachine(t, e.fleetEnv, first)
	seedMachine(t, e.fleetEnv, second)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var firstStage atomic.Bool
	e.revoker.onStage = func() {
		if firstStage.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { done <- e.revoke(context.Background(), "first", first, 1) }()
	<-entered
	if err := e.revoke(t.Context(), "second", second, 1); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("concurrent second revoke = %v, want quota refusal", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := e.revoker.stages.Load(); got != 1 {
		t.Errorf("Stage calls = %d, want 1", got)
	}
}

func TestTCPRevokeOldWindowRefundCannotCreditNewWindow(t *testing.T) {
	e := newTCPRevokeEnv(t, 1)
	first, second, third := "01jmachine0000000000000001", "01jmachine0000000000000002", "01jmachine0000000000000003"
	for _, id := range []string{first, second, third} {
		seedMachine(t, e.fleetEnv, id)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	var firstStage atomic.Bool
	e.revoker.onStage = func() {
		if firstStage.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { done <- e.revoke(context.Background(), "late", first, 1) }()
	<-entered
	e.clock.Store(fleetNow.Add(time.Minute + time.Second).UnixNano())
	if err := e.revoke(t.Context(), "second", second, 1); err != nil {
		t.Fatalf("new minute's first revoke: %v", err)
	}
	_, err := e.fleetEnv.client.UpdateMachine(t.Context(), connect.NewRequest(
		updateMachineRequest(first, "rename", "renamed", nil, 1, []string{"spec.display_name"})))
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("old minute's late revoke = %v, want stale version", err)
	}
	if err := e.revoke(t.Context(), "third", third, 1); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("old refund credited new minute: %v", err)
	}
}

func TestTCPRevokeCanceledAfterReservationStillChargesAndCuts(t *testing.T) {
	e := newTCPRevokeEnv(t, 1)
	first, second := "01jmachine0000000000000001", "01jmachine0000000000000002"
	seedMachine(t, e.fleetEnv, first)
	seedMachine(t, e.fleetEnv, second)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.revoker.onStage = cancel
	done := make(chan error, 1)
	go func() { done <- e.revoke(ctx, "first", first, 1) }()
	waitForBye(t, e.admin)
	<-done
	if !e.revoker.Revoked("ab0001") {
		t.Error("canceled request did not stage its serial")
	}
	if err := e.revoke(t.Context(), "second", second, 1); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("canceled admitted request did not charge: %v", err)
	}
}

func TestTCPRevokeCanceledAfterFileStageStillChargesAndCuts(t *testing.T) {
	e := newTCPRevokeEnv(t, 1)
	first, second := "01jmachine0000000000000001", "01jmachine0000000000000002"
	seedMachine(t, e.fleetEnv, first)
	seedMachine(t, e.fleetEnv, second)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.revoker.afterStage = cancel
	done := make(chan error, 1)
	go func() { done <- e.revoke(ctx, "first", first, 1) }()
	waitForBye(t, e.admin)
	<-done
	if !e.revoker.Revoked("ab0001") {
		t.Error("canceled request lost its staged serial")
	}
	if err := e.revoke(t.Context(), "second", second, 1); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("canceled staged request did not charge: %v", err)
	}
}

func TestTCPRevokeCanceledAfterCommitStillCutsAndCharges(t *testing.T) {
	e := newTCPRevokeEnv(t, 1)
	first, second := "01jmachine0000000000000001", "01jmachine0000000000000002"
	seedMachine(t, e.fleetEnv, first)
	seedMachine(t, e.fleetEnv, second)
	err := e.st.Write(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), `INSERT INTO commands(command_id, machine_id, kind, command,
			create_time, not_after_time, update_time, hub_seq) VALUES (?, ?, 'session.keys', ?, ?, ?, ?, 1)`,
			"queued-command", first, []byte("{}"), fleetNow.UnixMilli(),
			fleetNow.Add(time.Hour).UnixMilli(), fleetNow.UnixMilli())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var canceled atomic.Bool
	e.afterClosed = func() { // after final writer commit, before Bye
		canceled.Store(true)
		cancel()
	}
	done := make(chan error, 1)
	go func() { done <- e.revoke(ctx, "first", first, 1) }()
	waitForBye(t, e.admin)
	<-done
	if !canceled.Load() {
		t.Fatal("closed-command callback did not cancel the request")
	}
	if err := e.revoke(t.Context(), "second", second, 1); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("canceled committed request did not charge: %v", err)
	}
}
