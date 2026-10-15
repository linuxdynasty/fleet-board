package api_test

import (
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func accountSpec() *hubv1.AccountSpec {
	return &hubv1.AccountSpec{
		DisplayName: "work", Mode: hubv1.AccountMode_ACCOUNT_MODE_FLEET,
		BudgetKind: typev1.BudgetKind_BUDGET_KIND_DOLLAR_CAP,
	}
}

func createOwnerAccount(t *testing.T, e *fleetEnv, requestID string, spec *hubv1.AccountSpec) *hubv1.Account {
	t.Helper()
	res, err := e.client.CreateAccount(t.Context(), connect.NewRequest(&hubv1.CreateAccountRequest{
		RequestId: requestID, Harness: &typev1.Harness{Name: "opencode"},
		Provider: "anthropic", Spec: spec,
	}))
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return res.Msg.GetAccount()
}

func TestCreateAccountDefaultsReserveAndConsent(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	got := createOwnerAccount(t, e, "req-1", accountSpec())
	if got.GetVersion() != 1 {
		t.Errorf("version = %d, want 1", got.GetVersion())
	}
	if got.GetSpec().GetReservePercent() != 10 {
		t.Errorf("reserve = %d, want the default 10", got.GetSpec().GetReservePercent())
	}
	wantConsent := []typev1.ConsentAct{
		typev1.ConsentAct_CONSENT_ACT_PUSH,
		typev1.ConsentAct_CONSENT_ACT_PR_OPEN,
		typev1.ConsentAct_CONSENT_ACT_MERGE,
	}
	if len(got.GetSpec().GetConsent()) != len(wantConsent) {
		t.Fatalf("consent = %v, want push, pr-open and merge", got.GetSpec().GetConsent())
	}
	for i, c := range wantConsent {
		if got.GetSpec().GetConsent()[i] != c {
			t.Fatalf("consent = %v, want push, pr-open and merge", got.GetSpec().GetConsent())
		}
	}
	if got.GetStatus().GetOrigin() != hubv1.AccountOrigin_ACCOUNT_ORIGIN_OWNER_CREATED {
		t.Errorf("origin = %v, want owner-created", got.GetStatus().GetOrigin())
	}
}

func TestCreateAccountValidatesItsFields(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	cases := []struct {
		name      string
		requestID string
		harness   string
		provider  string
		spec      *hubv1.AccountSpec
		field     string
	}{
		{name: "request id required", requestID: "", harness: "opencode", provider: "anthropic", spec: accountSpec(), field: "request_id"},
		{name: "harness required", requestID: "r", harness: "", provider: "anthropic", spec: accountSpec(), field: "harness"},
		{name: "provider required", requestID: "r", harness: "opencode", provider: "", spec: accountSpec(), field: "provider"},
		{name: "dollar cap only", requestID: "r", harness: "opencode", provider: "anthropic", spec: func() *hubv1.AccountSpec {
			s := accountSpec()
			s.BudgetKind = typev1.BudgetKind_BUDGET_KIND_SUBSCRIPTION_PERCENT
			return s
		}(), field: "spec.budget_kind"},
		{name: "reserve capped", requestID: "r", harness: "opencode", provider: "anthropic", spec: func() *hubv1.AccountSpec {
			s := accountSpec()
			s.ReservePercent = 51
			return s
		}(), field: "spec.reserve_percent"},
		{name: "display name required", requestID: "r", harness: "opencode", provider: "anthropic", spec: func() *hubv1.AccountSpec {
			s := accountSpec()
			s.DisplayName = ""
			return s
		}(), field: "spec.display_name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := e.client.CreateAccount(t.Context(), connect.NewRequest(&hubv1.CreateAccountRequest{
				RequestId: tc.requestID, Harness: &typev1.Harness{Name: tc.harness},
				Provider: tc.provider, Spec: tc.spec,
			}))
			code, _, fields := detail(t, err)
			if code != connect.CodeInvalidArgument {
				t.Errorf("= %v, want INVALID_ARGUMENT", code)
			}
			found := false
			for _, f := range fields {
				if f == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("fields = %v, want %q", fields, tc.field)
			}
		})
	}
}

func TestCreateAccountRefusesDuplicatesAndReplays(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	first := createOwnerAccount(t, e, "req-1", accountSpec())
	_, err := e.client.CreateAccount(t.Context(), connect.NewRequest(&hubv1.CreateAccountRequest{
		RequestId: "req-2", Harness: &typev1.Harness{Name: "opencode"},
		Provider: "anthropic", Spec: accountSpec(),
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeAlreadyExists || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_ALREADY_EXISTS {
		t.Errorf("duplicate = %v/%v, want ALREADY_EXISTS/RESOURCE_ALREADY_EXISTS", code, reason)
	}
	res, err := e.client.CreateAccount(t.Context(), connect.NewRequest(&hubv1.CreateAccountRequest{
		RequestId: "req-1", Harness: &typev1.Harness{Name: "opencode"},
		Provider: "anthropic", Spec: accountSpec(),
	}))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if res.Msg.GetAccount().GetId().GetValue() != first.GetId().GetValue() {
		t.Errorf("replay id = %q, want %q", res.Msg.GetAccount().GetId().GetValue(), first.GetId().GetValue())
	}
	events := e.logs.events(t, "config.account_written")
	replayed := 0
	for _, m := range events {
		if m["replayed"] == true {
			replayed++
		}
	}
	if replayed != 1 {
		t.Errorf("replayed writes = %d, want 1", replayed)
	}
}

func TestUpdateAccountMasksAndVersions(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	first := createOwnerAccount(t, e, "req-1", accountSpec())
	res, err := e.client.UpdateAccount(t.Context(), connect.NewRequest(&hubv1.UpdateAccountRequest{
		RequestId: "req-2",
		Account: &hubv1.Account{
			Id:   first.GetId(),
			Spec: &hubv1.AccountSpec{ReservePercent: 25},
		},
		UpdateMask: fieldMask([]string{"spec.reserve_percent"}),
	}))
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	got := res.Msg.GetAccount()
	if got.GetVersion() != 2 || got.GetSpec().GetReservePercent() != 25 {
		t.Errorf("account = %+v, want version 2 with reserve 25", got)
	}
	if got.GetSpec().GetDisplayName() != "work" || len(got.GetSpec().GetConsent()) != 3 {
		t.Errorf("account = %+v, want the other fields kept", got.GetSpec())
	}
	bad := &hubv1.UpdateAccountRequest{
		RequestId: "req-3",
		Account: &hubv1.Account{
			Id:   first.GetId(),
			Spec: &hubv1.AccountSpec{ReservePercent: 30},
		},
		UpdateMask: fieldMask([]string{"spec.reserve_percent"}), ExpectedVersion: 1,
	}
	_, err = e.client.UpdateAccount(t.Context(), connect.NewRequest(bad))
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH {
		t.Errorf("stale = %v/%v, want FAILED_PRECONDITION/VERSION_MISMATCH", code, reason)
	}
	if metadataOf(t, err)["current_version"] != "2" {
		t.Errorf("current_version = %q, want 2", metadataOf(t, err)["current_version"])
	}
	_, err = e.client.UpdateAccount(t.Context(), connect.NewRequest(&hubv1.UpdateAccountRequest{
		RequestId: "req-4",
		Account: &hubv1.Account{
			Id:   &typev1.AccountId{Value: "missing"},
			Spec: &hubv1.AccountSpec{ReservePercent: 30},
		},
		UpdateMask: fieldMask([]string{"spec.reserve_percent"}),
	}))
	code, reason, _ = detail(t, err)
	if code != connect.CodeNotFound || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown = %v/%v, want NOT_FOUND/RESOURCE_NOT_FOUND", code, reason)
	}
	_, err = e.client.UpdateAccount(t.Context(), connect.NewRequest(&hubv1.UpdateAccountRequest{
		RequestId: "req-5",
		Account: &hubv1.Account{
			Id:   first.GetId(),
			Spec: &hubv1.AccountSpec{ReservePercent: 30},
		},
		UpdateMask: fieldMask([]string{"spec.harness"}),
	}))
	code, reason, _ = detail(t, err)
	if code != connect.CodeInvalidArgument || reason != typev1.ReasonCode_REASON_CODE_UPDATE_MASK_INVALID {
		t.Errorf("mask = %v/%v, want INVALID_ARGUMENT/UPDATE_MASK_INVALID", code, reason)
	}
	_, err = e.client.UpdateAccount(t.Context(), connect.NewRequest(&hubv1.UpdateAccountRequest{
		RequestId: "req-6",
		Account: &hubv1.Account{
			Id: first.GetId(),
		},
		UpdateMask: fieldMask([]string{"spec.reserve_percent"}),
	}))
	code, _, fields := detail(t, err)
	if code != connect.CodeInvalidArgument {
		t.Errorf("nil spec = %v, want INVALID_ARGUMENT", code)
	}
	found := false
	for _, f := range fields {
		if f == "spec" {
			found = true
		}
	}
	if !found {
		t.Errorf("fields = %v, want spec", fields)
	}
}

func TestUpdateAccountMapsCodexBindings(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	first := createOwnerAccount(t, e, "req-1", accountSpec())
	res, err := e.client.UpdateAccount(t.Context(), connect.NewRequest(&hubv1.UpdateAccountRequest{
		RequestId: "req-2",
		Account: &hubv1.Account{
			Id: first.GetId(),
			Spec: &hubv1.AccountSpec{Bindings: []*hubv1.AccountBindingSpec{
				{Machine: &typev1.MachineId{Value: "machine-1"}, Profile: "work"},
			}},
		},
		UpdateMask: fieldMask([]string{"spec.bindings"}),
	}))
	if err != nil {
		t.Fatalf("UpdateAccount bindings: %v", err)
	}
	bindings := res.Msg.GetAccount().GetSpec().GetBindings()
	if len(bindings) != 1 || bindings[0].GetMachine().GetValue() != "machine-1" {
		t.Errorf("bindings = %+v, want the mapped machine", bindings)
	}
	_, err = e.client.UpdateAccount(t.Context(), connect.NewRequest(&hubv1.UpdateAccountRequest{
		RequestId: "req-3",
		Account: &hubv1.Account{
			Id:   first.GetId(),
			Spec: &hubv1.AccountSpec{Bindings: []*hubv1.AccountBindingSpec{{}}},
		},
		UpdateMask: fieldMask([]string{"spec.bindings"}),
	}))
	code, _, fields := detail(t, err)
	if code != connect.CodeInvalidArgument {
		t.Errorf("= %v, want INVALID_ARGUMENT", code)
	}
	found := false
	for _, f := range fields {
		if f == "spec.bindings[0].machine" {
			found = true
		}
	}
	if !found {
		t.Errorf("fields = %v, want spec.bindings[0].machine", fields)
	}
}

func TestDeleteAccountRemoves(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	first := createOwnerAccount(t, e, "req-1", accountSpec())
	_, err := e.client.DeleteAccount(t.Context(), connect.NewRequest(&hubv1.DeleteAccountRequest{
		RequestId: "req-2", Id: first.GetId(), ExpectedVersion: 1,
	}))
	if err != nil {
		t.Fatalf("DeleteAccount: %v", err)
	}
	_, err = e.client.DeleteAccount(t.Context(), connect.NewRequest(&hubv1.DeleteAccountRequest{
		RequestId: "req-2", Id: first.GetId(), ExpectedVersion: 1,
	}))
	if err != nil {
		t.Errorf("delete replay = %v, want the same empty outcome", err)
	}
	_, err = e.client.DeleteAccount(t.Context(), connect.NewRequest(&hubv1.DeleteAccountRequest{
		RequestId: "req-3", Id: first.GetId(),
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeNotFound || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("second delete = %v/%v, want NOT_FOUND/RESOURCE_NOT_FOUND", code, reason)
	}
}

func TestAccountReserveBoundary(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	fifty := accountSpec()
	fifty.ReservePercent = 50
	got := createOwnerAccount(t, e, "req-50", fifty)
	if got.GetSpec().GetReservePercent() != 50 {
		t.Errorf("reserve = %d, want 50 stored", got.GetSpec().GetReservePercent())
	}
}

func TestUpdateAccountRenameCollisionIsAlreadyExists(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	first := createOwnerAccount(t, e, "req-1", accountSpec())
	other := accountSpec()
	other.DisplayName = "play"
	createOwnerAccount(t, e, "req-2", other)
	renamed := accountSpec()
	renamed.DisplayName = "play"
	_, err := e.client.UpdateAccount(t.Context(), connect.NewRequest(&hubv1.UpdateAccountRequest{
		RequestId: "req-3",
		Account: &hubv1.Account{
			Id:   first.GetId(),
			Spec: renamed,
		},
		UpdateMask: fieldMask([]string{"spec.display_name"}),
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeAlreadyExists || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_ALREADY_EXISTS {
		t.Errorf("rename = %v/%v, want ALREADY_EXISTS/RESOURCE_ALREADY_EXISTS", code, reason)
	}
}
