package api_test

import (
	"context"
	"crypto/rand"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"github.com/linuxdynasty/fleet-board/internal/hub/config"
	"github.com/linuxdynasty/fleet-board/internal/hub/secret"
)

type quotaWriteEnv struct {
	*fleetEnv
	tcp    hubv1connect.FleetServiceClient
	secret string
}

func newQuotaWriteEnv(t *testing.T, limit int) *quotaWriteEnv {
	t.Helper()
	return newLocalQuotaEnv(t, 100, limit)
}

func newLocalQuotaEnv(t *testing.T, readLimit, writeLimit int) *quotaWriteEnv {
	t.Helper()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	fleet, err := api.NewFleet(api.FleetOptions{
		Store: e.st, CACert: e.caDER, Telemetry: e.tel, Now: func() time.Time { return fleetNow },
		StateDir: e.dir, LocalTrusted: true, HubInstanceID: testInstanceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	quotas, err := api.NewQuotas(config.RateLimits{
		Read: readLimit, Write: writeLimit, Unauthenticated: 100, Metrics: 100,
	}, e.tel, func() time.Time { return fleetNow })
	if err != nil {
		t.Fatal(err)
	}
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		t.Fatalf("unexpected listener address type %T", listener.Addr())
	}
	port := tcpAddr.Port
	srv, err := api.NewLocalServer(e.tel, port, fleet, quotas)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	httpSrv := httptest.NewUnstartedServer(srv.Handler)
	httpSrv.Listener = listener
	httpSrv.Start()
	t.Cleanup(httpSrv.Close)
	secretMu.Lock()
	_, err = secret.Ensure(e.dir, rand.Read)
	secretMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	value, err := secret.Reveal(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	return &quotaWriteEnv{
		fleetEnv: e, tcp: hubv1connect.NewFleetServiceClient(httpSrv.Client(), httpSrv.URL), secret: value,
	}
}

func (e *quotaWriteEnv) protectedCreateAccount(id, name string) *connect.Request[hubv1.CreateAccountRequest] {
	spec := accountSpec()
	spec.DisplayName = name
	req := connect.NewRequest(&hubv1.CreateAccountRequest{
		RequestId: id, Harness: &typev1.Harness{Name: "opencode"},
		Provider: "anthropic", Spec: spec,
	})
	req.Header().Set(api.SecretHeader, e.secret)
	return req
}

func TestLocalWriteQuotaAccountsCreateAndReplay(t *testing.T) {
	e := newQuotaWriteEnv(t, 1)
	first, err := e.tcp.CreateAccount(context.Background(), e.protectedCreateAccount("first", "first"))
	if err != nil {
		t.Fatalf("first account: %v", err)
	}
	if first.Msg.GetAccount().GetVersion() != 1 {
		t.Fatalf("first version = %d, want 1", first.Msg.GetAccount().GetVersion())
	}
	_, err = e.tcp.CreateAccount(context.Background(), e.protectedCreateAccount("second", "second"))
	code, reason, _ := detail(t, err)
	if code != connect.CodeResourceExhausted || reason != typev1.ReasonCode_REASON_CODE_RATE_LIMITED {
		t.Errorf("second account = %v/%v, want RESOURCE_EXHAUSTED/RATE_LIMITED", code, reason)
	}
	if got := quotaReceiptCount(t, e, "config", "second"); got != 0 {
		t.Errorf("refused create receipt count = %d, want 0", got)
	}
	_, err = e.tcp.CreateAccount(context.Background(), e.protectedCreateAccount("first", "first"))
	code, reason, _ = detail(t, err)
	if code != connect.CodeResourceExhausted || reason != typev1.ReasonCode_REASON_CODE_RATE_LIMITED {
		t.Errorf("replay = %v/%v, want RESOURCE_EXHAUSTED/RATE_LIMITED", code, reason)
	}
	if got := len(e.auditRowText(t, "account.create")); got != 1 {
		t.Errorf("account.create audit rows = %d, want 1", got)
	}
	_, err = e.tcp.CreateAccount(context.Background(), e.protectedCreateAccount("first", "different"))
	code, reason, _ = detail(t, err)
	if code != connect.CodeAlreadyExists || reason != typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT {
		t.Errorf("conflicting request ID = %v/%v, want ALREADY_EXISTS/REQUEST_ID_CONFLICT", code, reason)
	}
	bad := e.protectedCreateAccount("bad", "")
	_, err = e.tcp.CreateAccount(context.Background(), bad)
	code, _, _ = detail(t, err)
	if code != connect.CodeInvalidArgument {
		t.Errorf("invalid account = %v, want INVALID_ARGUMENT", code)
	}
	withoutSecret := e.protectedCreateAccount("without-secret", "third")
	withoutSecret.Header().Del(api.SecretHeader)
	_, err = e.tcp.CreateAccount(context.Background(), withoutSecret)
	wantQuotaCode(t, err, connect.CodePermissionDenied, typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID)
}

func TestLocalWriteQuotaAccountsUpdateAndDelete(t *testing.T) {
	for _, tc := range []struct {
		name   string
		invoke func(*quotaWriteEnv, string, *typev1.AccountId, uint64) error
		action string
	}{
		{
			name: "update", action: "account.update",
			invoke: func(e *quotaWriteEnv, requestID string, id *typev1.AccountId, version uint64) error {
				req := connect.NewRequest(&hubv1.UpdateAccountRequest{
					RequestId:  requestID,
					Account:    &hubv1.Account{Id: id, Spec: &hubv1.AccountSpec{ReservePercent: 25}},
					UpdateMask: fieldMask([]string{"spec.reserve_percent"}), ExpectedVersion: version,
				})
				req.Header().Set(api.SecretHeader, e.secret)
				_, err := e.tcp.UpdateAccount(context.Background(), req)
				return err
			},
		},
		{
			name: "delete", action: "account.delete",
			invoke: func(e *quotaWriteEnv, requestID string, id *typev1.AccountId, version uint64) error {
				req := connect.NewRequest(&hubv1.DeleteAccountRequest{
					RequestId: requestID, Id: id, ExpectedVersion: version,
				})
				req.Header().Set(api.SecretHeader, e.secret)
				_, err := e.tcp.DeleteAccount(context.Background(), req)
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newQuotaWriteEnv(t, 1)
			first := createOwnerAccount(t, e.fleetEnv, "setup-1", accountSpec())
			secondSpec := accountSpec()
			secondSpec.DisplayName = "second"
			second := createOwnerAccount(t, e.fleetEnv, "setup-2", secondSpec)
			if err := tc.invoke(e, "first", first.GetId(), 1); err != nil {
				t.Fatalf("first %s: %v", tc.name, err)
			}
			if err := tc.invoke(e, "second", second.GetId(), 1); err == nil {
				t.Fatal("second write succeeded after its budget was spent")
			} else if code, reason, _ := detail(t, err); code != connect.CodeResourceExhausted || reason != typev1.ReasonCode_REASON_CODE_RATE_LIMITED {
				t.Errorf("second write = %v/%v, want RESOURCE_EXHAUSTED/RATE_LIMITED", code, reason)
			}
			if got := quotaReceiptCount(t, e, "config", "second"); got != 0 {
				t.Errorf("refused %s receipt count = %d, want 0", tc.name, got)
			}
			if err := tc.invoke(e, "first", first.GetId(), 1); err == nil {
				t.Fatal("replay succeeded after its budget was spent")
			} else if code, _, _ := detail(t, err); code != connect.CodeResourceExhausted {
				t.Errorf("replay = %v, want RESOURCE_EXHAUSTED", code)
			}
			if err := tc.invoke(e, "missing", &typev1.AccountId{Value: "missing"}, 0); err == nil {
				t.Fatal("missing account unexpectedly succeeded")
			} else if code, _, _ := detail(t, err); code != connect.CodeNotFound {
				t.Errorf("missing account = %v, want NOT_FOUND", code)
			}
			if err := tc.invoke(e, "stale", second.GetId(), 9); err == nil {
				t.Fatal("stale account unexpectedly succeeded")
			} else if code, _, _ := detail(t, err); code != connect.CodeFailedPrecondition {
				t.Errorf("stale account = %v, want FAILED_PRECONDITION", code)
			}
			if got := len(e.auditRowText(t, tc.action)); got != 1 {
				t.Errorf("%s audit rows = %d, want 1", tc.action, got)
			}
		})
	}
}
