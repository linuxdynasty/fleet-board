package api_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func fleetSettingsScope() *hubv1.SettingsScope {
	return &hubv1.SettingsScope{Kind: &hubv1.SettingsScope_Fleet_{Fleet: &hubv1.SettingsScope_Fleet{}}}
}

func machineSettingsScope(id string) *hubv1.SettingsScope {
	return &hubv1.SettingsScope{Kind: &hubv1.SettingsScope_Machine{Machine: &typev1.MachineId{Value: id}}}
}

// seedFleetLayer writes the fleet settings layer through the production
// write path: max_active 2 with auto_dispatch on.
func seedFleetLayer(t *testing.T, e *fleetEnv) {
	t.Helper()
	maxActive := uint32(2)
	updateFleetSettings(t, e, "req-settings-1", &hubv1.SettingsSpec{
		Scope:  fleetSettingsScope(),
		Values: &typev1.SettingValues{MaxActive: &maxActive},
		Hub:    &hubv1.HubSettings{AutoDispatch: func() *bool { b := true; return &b }()},
	}, []string{"spec.values.max_active", "spec.hub.auto_dispatch"}, 0)
}

// seedMachineLayer stores a machine settings layer through the
// production import path: the enrolled laptop's layer holds max_active
// 4. It returns the machine id.
func seedMachineLayer(t *testing.T, e *fleetEnv) string {
	t.Helper()
	id := "01jsettingsmachine00001"
	seedMachine(t, e, id)
	four := uint32(4)
	doc := &hubv1.FleetConfig{FormatVersion: 1, Machines: []*hubv1.MachineConfig{{
		Name: "laptop", Labels: []string{"gpu"}, Values: &typev1.SettingValues{MaxActive: &four},
	}}}
	plan := dryRun(t, e, "dry-settings-1", doc)
	res := applyDoc(t, e, "apply-settings-1", doc, plan.GetPlanHash())
	if !res.GetApplied() {
		t.Fatal("import apply did not store the machine layer")
	}
	return id
}

func getSettings(t *testing.T, e *fleetEnv, scope *hubv1.SettingsScope) *hubv1.Settings {
	t.Helper()
	resp, err := e.client.GetSettings(context.Background(), connect.NewRequest(&hubv1.GetSettingsRequest{Scope: scope}))
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	return resp.Msg.GetSettings()
}

func listSettings(t *testing.T, e *fleetEnv, req *hubv1.ListSettingsRequest) *hubv1.ListSettingsResponse {
	t.Helper()
	resp, err := e.client.ListSettings(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("ListSettings: %v", err)
	}
	return resp.Msg
}

func settingsIDs(msg *hubv1.ListSettingsResponse) []string {
	var out []string
	for _, s := range msg.GetSettings() {
		out = append(out, s.GetId().GetValue())
	}
	return out
}

// TestGetSettingsServesTheFleetLayer is 02k's fleet settings read: the
// stored spec with its version and stamps.
func TestGetSettingsServesTheFleetLayer(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedFleetLayer(t, e)

	got := getSettings(t, e, fleetSettingsScope())
	if got.GetId().GetValue() != "fleet" {
		t.Errorf("id %q, want fleet", got.GetId().GetValue())
	}
	if got.GetVersion() != 1 {
		t.Errorf("version %d, want 1", got.GetVersion())
	}
	if !got.GetCreateTime().IsValid() || !got.GetUpdateTime().IsValid() {
		t.Error("create_time and update_time unset, want the write stamps")
	}
	if got.GetSpec().GetValues().GetMaxActive() != 2 || !got.GetSpec().GetHub().GetAutoDispatch() {
		t.Errorf("spec %+v, want max_active 2 with auto_dispatch", got.GetSpec())
	}
	if got.GetSpec().GetScope().GetFleet() == nil {
		t.Errorf("scope %+v, want the fleet layer identity", got.GetSpec().GetScope())
	}
	if !got.GetStatus().GetObservedTime().IsValid() || got.GetStatus().GetHubSeq() == 0 {
		t.Errorf("status %+v, want the write stamp", got.GetStatus())
	}
}

// TestGetSettingsServesAMachineLayer reads a machine layer the import
// stored: the id names the machine, the scope is the machine layer,
// and the values are the entry's.
func TestGetSettingsServesAMachineLayer(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	id := seedMachineLayer(t, e)

	got := getSettings(t, e, machineSettingsScope(id))
	if got.GetId().GetValue() != "machine/"+id {
		t.Errorf("id %q, want machine/%s", got.GetId().GetValue(), id)
	}
	if got.GetVersion() != 1 {
		t.Errorf("version %d, want 1", got.GetVersion())
	}
	if got.GetSpec().GetScope().GetMachine().GetValue() != id {
		t.Errorf("scope %+v, want the machine layer identity", got.GetSpec().GetScope())
	}
	if got.GetSpec().GetValues().GetMaxActive() != 4 {
		t.Errorf("max_active %d, want the imported 4", got.GetSpec().GetValues().GetMaxActive())
	}
}

// TestGetSettingsMissesAndRefusesBadScopes covers the scope checks: a
// missing scope, a layer nothing stored, and a malformed scope.
func TestGetSettingsMissesAndRefusesBadScopes(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedFleetLayer(t, e)

	for _, tc := range []struct {
		name   string
		scope  *hubv1.SettingsScope
		code   connect.Code
		reason typev1.ReasonCode
		field  string // empty: NOT_FOUND carries no field
	}{
		{"fleet never written misses", fleetSettingsScope(), 0, 0, ""}, // replaced below
		{"unknown machine misses", machineSettingsScope("01jsettingsmissing00001"), 0, 0, ""},
		{"group with no row misses", groupScope("gpu"), 0, 0, ""},
		{"no scope refused", nil, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "scope"},
		{"empty scope refused", &hubv1.SettingsScope{}, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "scope"},
		{"machine id with a slash refused", machineSettingsScope("../01jabc"), connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "scope.machine.value"},
		{"upper-case group refused", groupScope("GPU"), connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_NAME, "scope.group_label"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.name == "fleet never written misses" {
				fresh := newFleetEnv(t, localUser("alice"), trusted(false))
				_, err := fresh.client.GetSettings(context.Background(), connect.NewRequest(&hubv1.GetSettingsRequest{Scope: tc.scope}))
				if code, _, _ := detail(t, err); code != connect.CodeNotFound {
					t.Errorf("fresh fleet: %v, want NOT_FOUND", code)
				}
				return
			}
			_, err := e.client.GetSettings(context.Background(), connect.NewRequest(&hubv1.GetSettingsRequest{Scope: tc.scope}))
			code, reason, fields := detail(t, err)
			if tc.code == 0 {
				if code != connect.CodeNotFound || reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
					t.Errorf("got %v %v, want NOT_FOUND RESOURCE_NOT_FOUND", code, reason)
				}
				return
			}
			if code != tc.code || reason != tc.reason {
				t.Errorf("got %v %v, want %v", code, reason, tc.reason)
			}
			found := false
			for _, f := range fields {
				if f == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("fields %v, want %q", fields, tc.field)
			}
		})
	}
}

// TestListSettingsServesStoredLayers is 02k's settings list: every
// stored layer with its spec, in id order by default.
func TestListSettingsServesStoredLayers(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedFleetLayer(t, e)
	id := seedMachineLayer(t, e)

	msg := listSettings(t, e, &hubv1.ListSettingsRequest{})
	if got := settingsIDs(msg); len(got) != 2 || got[0] != "fleet" || got[1] != "machine/"+id {
		t.Fatalf("ids %v, want [fleet machine/%s] in id order", got, id)
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	fleet := msg.GetSettings()[0]
	if fleet.GetVersion() != 1 || fleet.GetSpec().GetValues().GetMaxActive() != 2 {
		t.Errorf("fleet %+v, want version 1 with max_active 2", fleet)
	}
	machine := msg.GetSettings()[1]
	if machine.GetSpec().GetValues().GetMaxActive() != 4 {
		t.Errorf("machine max_active %d, want the imported 4", machine.GetSpec().GetValues().GetMaxActive())
	}

	fresh := listSettings(t, newFleetEnv(t, localUser("alice"), trusted(false)), &hubv1.ListSettingsRequest{})
	if len(fresh.GetSettings()) != 0 || fresh.GetNextPageToken() != "" {
		t.Errorf("fresh store holds %d layers, want 0 with no token", len(fresh.GetSettings()))
	}
}

// TestListSettingsFiltersPagesAndOrders covers the 02c Settings
// catalogue: the layer filter, ordering and paging.
func TestListSettingsFiltersPagesAndOrders(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedFleetLayer(t, e)
	seedMachineLayer(t, e)

	cases := []struct {
		name   string
		filter string
		wantN  int
	}{
		{"fleet", `layer = "fleet"`, 1},
		{"machine", `layer = "machine"`, 1},
		{"group misses", `layer = "group"`, 0},
		{"layer in", `layer in ("fleet", "machine")`, 2},
		{"layer in misses", `layer in ("group")`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listSettings(t, e, &hubv1.ListSettingsRequest{Filter: tc.filter})
			if len(msg.GetSettings()) != tc.wantN {
				t.Errorf("filter %q matched %d, want %d", tc.filter, len(msg.GetSettings()), tc.wantN)
			}
		})
	}

	byCT := listSettings(t, e, &hubv1.ListSettingsRequest{OrderBy: "ct"})
	if got := settingsIDs(byCT); len(got) != 2 || got[0] != "fleet" {
		t.Errorf("order_by ct ids %v, want fleet first with the id tiebreak", got)
	}

	first := listSettings(t, e, &hubv1.ListSettingsRequest{PageSize: 1})
	if len(first.GetSettings()) != 1 || first.GetNextPageToken() == "" {
		t.Fatalf("page 1 holds %d layers with token %q, want 1 layer and a token",
			len(first.GetSettings()), first.GetNextPageToken())
	}
	second := listSettings(t, e, &hubv1.ListSettingsRequest{PageSize: 1, PageToken: first.GetNextPageToken()})
	if len(second.GetSettings()) != 1 || second.GetNextPageToken() != "" {
		t.Fatalf("page 2 holds %d layers with token %q, want the last layer and no token",
			len(second.GetSettings()), second.GetNextPageToken())
	}
	if got := second.GetSettings()[0].GetId().GetValue(); got == "fleet" {
		t.Errorf("page 2 id %q, want the machine layer", got)
	}

	_, err := e.client.ListSettings(context.Background(), connect.NewRequest(
		&hubv1.ListSettingsRequest{PageSize: 1, PageToken: first.GetNextPageToken(), Filter: `layer = "fleet"`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: %v %v, want INVALID_ARGUMENT PAGE_TOKEN_INVALID", code, reason)
	}

	_, err = e.client.ListSettings(context.Background(), connect.NewRequest(
		&hubv1.ListSettingsRequest{Filter: `id = "fleet"`}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
		t.Errorf("id filter: %v %v, want INVALID_ARGUMENT FILTER_INVALID", code, reason)
	}

	_, err = e.client.ListSettings(context.Background(), connect.NewRequest(
		&hubv1.ListSettingsRequest{OrderBy: "layer"}))
	if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID {
		t.Errorf("order_by layer: %v %v, want INVALID_ARGUMENT ORDER_BY_INVALID", code, reason)
	}
}

// TestSettingsReadsRefuseBadCallers covers the caller checks: no
// caller, and a signed-in user without READ.
func TestSettingsReadsRefuseBadCallers(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedFleetLayer(t, e)

	anon := newFleetEnv(t, nil)
	_, err := anon.client.GetSettings(context.Background(), connect.NewRequest(&hubv1.GetSettingsRequest{Scope: fleetSettingsScope()}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("get no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}
	_, err = anon.client.ListSettings(context.Background(), connect.NewRequest(&hubv1.ListSettingsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodeUnauthenticated ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("list no caller: %v %v, want UNAUTHENTICATED CREDENTIALS_INVALID", code, reason)
	}

	user := newFleetEnv(t, &typev1.Principal{
		Name: "bob", Role: typev1.Role_ROLE_OWNER, Kind: typev1.PrincipalKind_PRINCIPAL_KIND_USER,
		Id: &typev1.PrincipalId{Value: "01jabcdefghjkmnpqrstvwxyz0"},
	})
	_, err = user.client.GetSettings(context.Background(), connect.NewRequest(&hubv1.GetSettingsRequest{Scope: fleetSettingsScope()}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("get signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
	_, err = user.client.ListSettings(context.Background(), connect.NewRequest(&hubv1.ListSettingsRequest{}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("list signed-in user: %v %v, want PERMISSION_DENIED SCOPE_MISSING", code, reason)
	}
}
