package api_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// enrollMachine enrolls one machine straight into the env's store: a token
// row, then the Enroll write. body keys the token secret and the CSR hash so
// parallel subtests never share a token.
// TestListMachinesServesEnrolledMachines is 02k's machine read: every
// enrolled machine with its spec and the stored status, in name order.
func TestListMachinesServesEnrolledMachines(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollNamedMachine(t, e, 11, "beta", "m-beta", false)
	enrollNamedMachine(t, e, 12, "alpha", "m-alpha", false)

	msg := listMachines(t, e, &hubv1.ListMachinesRequest{})
	if len(msg.GetMachines()) != 2 {
		t.Fatalf("machines %d, want 2 enrolled", len(msg.GetMachines()))
	}
	if msg.GetNextPageToken() != "" {
		t.Errorf("next_page_token %q, want empty on a single page", msg.GetNextPageToken())
	}
	first, second := msg.GetMachines()[0], msg.GetMachines()[1]
	if first.GetSpec().GetDisplayName() != "alpha" || second.GetSpec().GetDisplayName() != "beta" {
		t.Errorf("order %q, %q, want alpha then beta (default order is name)",
			first.GetSpec().GetDisplayName(), second.GetSpec().GetDisplayName())
	}
	if first.GetId().GetValue() != "m-alpha" {
		t.Errorf("id %q, want m-alpha", first.GetId().GetValue())
	}
	if first.GetVersion() != 1 {
		t.Errorf("version %d, want 1 (no spec mutation exists yet)", first.GetVersion())
	}
	if got := first.GetSpec().GetLabels(); len(got) != 1 || got[0] != "gpu" {
		t.Errorf("labels %v, want [gpu] from the enroll token", got)
	}
	if first.GetStatus().GetState() != hubv1.MachineState_MACHINE_STATE_ENROLLED {
		t.Errorf("state %s, want ENROLLED", first.GetStatus().GetState())
	}
	if first.GetStatus().GetHostname() != "alpha.lan" {
		t.Errorf("hostname %q, want the enrolled facts", first.GetStatus().GetHostname())
	}
	if first.GetStatus().GetOs() != "linux" || first.GetStatus().GetArch() != "arm64" {
		t.Errorf("os/arch %q/%q, want linux/arm64 from the enrolled facts",
			first.GetStatus().GetOs(), first.GetStatus().GetArch())
	}
	if first.GetStatus().GetInstallId() != "install-m-alpha" {
		t.Errorf("install_id %q, want install-m-alpha", first.GetStatus().GetInstallId())
	}
	if !first.GetCreateTime().IsValid() || !first.GetUpdateTime().IsValid() {
		t.Error("create_time and update_time are unset, want the enroll time")
	}
}

// TestListMachinesFiltersByStateAndName covers the 02c machine filter: one
// enum filter and one string filter.
func TestListMachinesFiltersByStateAndName(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollNamedMachine(t, e, 21, "alpha", "m-alpha", false)
	enrollNamedMachine(t, e, 22, "beta", "m-beta", false)

	for _, tc := range []struct {
		name   string
		filter string
		want   []string
	}{
		{"state-equals", `state = ENROLLED`, []string{"alpha", "beta"}},
		{"state-no-match", `state = ONLINE`, nil},
		{"state-in", `state in (ENROLLED, ONLINE)`, []string{"alpha", "beta"}},
		{"state-not-equals", `state != ONLINE`, []string{"alpha", "beta"}},
		{"name-equals", `name = "beta"`, []string{"beta"}},
		{"name-and-state", `name = "beta" AND state = ENROLLED`, []string{"beta"}},
		{"label-has", `label:"gpu"`, []string{"alpha", "beta"}},
		{"label-miss", `label:"tpu"`, nil},
		{"not", `NOT state = ONLINE`, []string{"alpha", "beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			msg := listMachines(t, e, &hubv1.ListMachinesRequest{Filter: tc.filter})
			var got []string
			for _, m := range msg.GetMachines() {
				got = append(got, m.GetSpec().GetDisplayName())
			}
			if len(got) != len(tc.want) {
				t.Fatalf("filter %q matched %v, want %v", tc.filter, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("filter %q matched %v, want %v", tc.filter, got, tc.want)
				}
			}
		})
	}
}

// TestListMachinesPages walks three machines one row at a time: every row
// shows up exactly once and the token of the last page is empty.
func TestListMachinesPages(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollNamedMachine(t, e, 31, "alpha", "m-alpha", false)
	enrollNamedMachine(t, e, 32, "beta", "m-beta", false)
	enrollNamedMachine(t, e, 33, "gamma", "m-gamma", false)

	var names []string
	token := ""
	for i := 0; i < 5; i++ {
		msg := listMachines(t, e, &hubv1.ListMachinesRequest{PageSize: 1, PageToken: token})
		for _, m := range msg.GetMachines() {
			names = append(names, m.GetSpec().GetDisplayName())
		}
		token = msg.GetNextPageToken()
		if token == "" {
			break
		}
	}
	if len(names) != 3 || names[0] != "alpha" || names[1] != "beta" || names[2] != "gamma" {
		t.Errorf("paged names %v, want [alpha beta gamma] exactly once", names)
	}

	if _, err := e.client.ListMachines(context.Background(),
		connect.NewRequest(&hubv1.ListMachinesRequest{PageToken: "bogus"})); err == nil {
		t.Error("a bogus page_token succeeded, want PAGE_TOKEN_INVALID")
	} else if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("bogus token: code %s reason %s, want INVALID_ARGUMENT/PAGE_TOKEN_INVALID", code, reason)
	}

	other := listMachines(t, e, &hubv1.ListMachinesRequest{PageSize: 1})
	if _, err := e.client.ListMachines(context.Background(), connect.NewRequest(
		&hubv1.ListMachinesRequest{PageSize: 2, PageToken: other.GetNextPageToken()})); err == nil {
		t.Error("a token from another query succeeded, want PAGE_TOKEN_INVALID")
	} else if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("foreign token: code %s reason %s, want INVALID_ARGUMENT/PAGE_TOKEN_INVALID", code, reason)
	}
}

// TestListMachinesRefusesBadFilterAndOrder pins the grammar edge: unknown
// names, bad values, unserved fields and bad ordering are FILTER_INVALID or
// ORDER_BY_INVALID, never an empty list mistaken for no machines.
func TestListMachinesRefusesBadFilterAndOrder(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollNamedMachine(t, e, 41, "alpha", "m-alpha", false)

	for _, tc := range []struct {
		name   string
		filter string
		order  string
		reason typev1.ReasonCode
		offset string
	}{
		{"unknown-field", `bogus = 1`, "", typev1.ReasonCode_REASON_CODE_FILTER_INVALID, "0"},
		{"bad-enum", `state = BOGUS`, "", typev1.ReasonCode_REASON_CODE_FILTER_INVALID, "8"},
		{"bad-op", `name > "a"`, "", typev1.ReasonCode_REASON_CODE_FILTER_INVALID, "0"},
		{"unbalanced", `(state = ENROLLED`, "", typev1.ReasonCode_REASON_CODE_FILTER_INVALID, "9"},
		{"late-unknown-field", `state = ENROLLED OR bogus = 1`, "", typev1.ReasonCode_REASON_CODE_FILTER_INVALID, "20"},
		{"unserved-harness", `harness:"cuda"`, "", typev1.ReasonCode_REASON_CODE_FILTER_INVALID, "0"},
		{"unserved-is-hub", `is_hub = true`, "", typev1.ReasonCode_REASON_CODE_FILTER_INVALID, "0"},
		{"unknown-order", "", "bogus", typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, ""},
		{"too-many-orders", "", "name, ct, last_seen_time, name", typev1.ReasonCode_REASON_CODE_ORDER_BY_INVALID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := e.client.ListMachines(context.Background(), connect.NewRequest(
				&hubv1.ListMachinesRequest{Filter: tc.filter, OrderBy: tc.order}))
			if err == nil {
				t.Fatalf("filter %q order %q succeeded, want %s", tc.filter, tc.order, tc.reason)
			}
			code, reason, fields := detail(t, err)
			if code != connect.CodeInvalidArgument || reason != tc.reason {
				t.Errorf("code %s reason %s fields %v, want INVALID_ARGUMENT/%s",
					code, reason, fields, tc.reason)
			}
			if len(fields) == 0 || fields[0] != "filter" && fields[0] != "order_by" {
				t.Errorf("violation fields %v, want the bad field named", fields)
			}
			if tc.offset != "" {
				if got := errorOffset(t, err); got != tc.offset {
					t.Errorf("metadata offset %q, want %q pointing at the bad atom", got, tc.offset)
				}
			}
		})
	}
}

// TestGetMachineReadsOneRow covers GetMachine: the enrolled row by id, and
// NOT_FOUND for an id the hub never enrolled.
func TestGetMachineReadsOneRow(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollNamedMachine(t, e, 51, "alpha", "m-alpha", false)

	resp, err := e.client.GetMachine(context.Background(),
		connect.NewRequest(&hubv1.GetMachineRequest{Id: &typev1.MachineId{Value: "m-alpha"}}))
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	if got := resp.Msg.GetMachine().GetSpec().GetDisplayName(); got != "alpha" {
		t.Errorf("display_name %q, want alpha", got)
	}

	_, err = e.client.GetMachine(context.Background(),
		connect.NewRequest(&hubv1.GetMachineRequest{Id: &typev1.MachineId{Value: "m-ghost"}}))
	if err == nil {
		t.Fatal("GetMachine for an unknown id succeeded, want NOT_FOUND")
	}
	if code, reason, _ := detail(t, err); code != connect.CodeNotFound ||
		reason != typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND {
		t.Errorf("unknown id: code %s reason %s, want NOT_FOUND/RESOURCE_NOT_FOUND", code, reason)
	}

	_, err = e.client.GetMachine(context.Background(),
		connect.NewRequest(&hubv1.GetMachineRequest{}))
	if err == nil {
		t.Fatal("GetMachine without an id succeeded, want INVALID_ARGUMENT")
	}
	if code, _, _ := detail(t, err); code != connect.CodeInvalidArgument {
		t.Errorf("missing id: code %s, want INVALID_ARGUMENT", code)
	}
}

// TestMachineReadsNeedALocalCaller pins the 02d READ gate: no caller is
// unauthenticated, and a caller that is not the local user is refused until
// a later row-12 slice brings caller scopes.
func TestMachineReadsNeedALocalCaller(t *testing.T) {
	t.Parallel()
	anon := newFleetEnv(t, nil)
	if _, err := anon.client.ListMachines(context.Background(),
		connect.NewRequest(&hubv1.ListMachinesRequest{})); err == nil {
		t.Error("ListMachines without a caller succeeded, want UNAUTHENTICATED")
	} else if code, _, _ := detail(t, err); code != connect.CodeUnauthenticated {
		t.Errorf("no caller: code %s, want UNAUTHENTICATED", code)
	}

	remote := &typev1.Principal{
		Name: "bot", Role: typev1.Role_ROLE_VIEWER,
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_API_TOKEN,
		Id:   &typev1.PrincipalId{Value: "token:1"},
	}
	wrong := newFleetEnv(t, remote)
	if _, err := wrong.client.GetMachine(context.Background(), connect.NewRequest(
		&hubv1.GetMachineRequest{Id: &typev1.MachineId{Value: "m-alpha"}})); err == nil {
		t.Error("GetMachine for a non-local caller succeeded, want PERMISSION_DENIED")
	} else if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Errorf("non-local caller: code %s reason %s, want PERMISSION_DENIED/SCOPE_MISSING", code, reason)
	}
}

// TestUnbuiltReadsStayUnimplemented pins 02k's out-of-release rule for the
// reads this slice leaves alone: a v3 RPC and a v1 read whose row has not
// landed answer UNIMPLEMENTED, not a connection error.
func TestUnbuiltReadsStayUnimplemented(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	_, err := e.client.RedeemInvite(context.Background(), connect.NewRequest(&hubv1.RedeemInviteRequest{}))
	if code, _, _ := detail(t, err); code != connect.CodeUnimplemented {
		t.Errorf("RedeemInvite code %s, want UNIMPLEMENTED (v3, no row builds it)", code)
	}
	_, err = e.client.ListChangeRequests(context.Background(), connect.NewRequest(&hubv1.ListChangeRequestsRequest{}))
	if code, _, _ := detail(t, err); code != connect.CodeUnimplemented {
		t.Errorf("ListChangeRequests code %s, want UNIMPLEMENTED (row 12, later slice)", code)
	}
}

// TestListMachinesCoversTheRestOfTheCatalogue exercises what the contract
// tests above do not: the draining and time filters, OR with parentheses,
// every order_by name, and the grammar limits.
func TestListMachinesCoversTheRestOfTheCatalogue(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollNamedMachine(t, e, 61, "alpha", "m-alpha", false)
	enrollNamedMachine(t, e, 62, "beta", "m-beta", false)
	enrollNamedMachine(t, e, 63, "gamma", "m-gamma", true)

	names := func(filter, order string) []string {
		t.Helper()
		msg := listMachines(t, e, &hubv1.ListMachinesRequest{Filter: filter, OrderBy: order})
		var out []string
		for _, m := range msg.GetMachines() {
			out = append(out, m.GetSpec().GetDisplayName())
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		filter string
		order  string
		want   []string
	}{
		{"draining", `draining = true`, "", []string{"gamma"}},
		{"not-draining", `draining = false`, "", []string{"alpha", "beta"}},
		{"created-after", `create_time >= "2026-10-06T05:02:00Z"`, "", []string{"beta", "gamma"}},
		{"created-before", `create_time < "2026-10-06T05:02:00Z"`, "", []string{"alpha"}},
		{"updated-is-created", `update_time = "2026-10-06T05:01:00Z"`, "", []string{"alpha"}},
		{"or", `name = "alpha" OR name = "gamma"`, "", []string{"alpha", "gamma"}},
		{"parens", `(name = "alpha" OR name = "beta") AND draining = false`, "", []string{"alpha", "beta"}},
		{"by-name-desc", ``, "name desc", []string{"gamma", "beta", "alpha"}},
		{"by-ct", ``, "ct", []string{"alpha", "beta", "gamma"}},
		{"by-ct-desc", ``, "ct desc", []string{"gamma", "beta", "alpha"}},
		{"by-seen-then-id", ``, "last_seen_time", []string{"alpha", "beta", "gamma"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := names(tc.filter, tc.order); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("filter %q order %q matched %v, want %v", tc.filter, tc.order, got, tc.want)
			}
		})
	}

	bad := map[string]string{
		"too-many-terms": strings.Repeat(`state = ENROLLED AND `, 33) + `state = ENROLLED`,
		"too-deep":       `((((((state = ENROLLED))))))`,
		"too-big":        `name = "` + strings.Repeat("x", 5000) + `"`,
	}
	var inVals []string
	for i := 0; i < 201; i++ {
		inVals = append(inVals, "ENROLLED")
	}
	bad["too-many-values"] = `state in (` + strings.Join(inVals, ",") + `)`
	for name, filter := range bad {
		if _, err := e.client.ListMachines(context.Background(),
			connect.NewRequest(&hubv1.ListMachinesRequest{Filter: filter})); err == nil {
			t.Errorf("%s: oversized filter succeeded, want FILTER_INVALID", name)
		} else if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
			reason != typev1.ReasonCode_REASON_CODE_FILTER_INVALID {
			t.Errorf("%s: code %s reason %s, want INVALID_ARGUMENT/FILTER_INVALID", name, code, reason)
		}
	}
}

// TestListMachinesRefusesForgedTokens pins the two token attacks: a token
// minted for the query with its sort key cut short, and a token over 4 KiB.
func TestListMachinesRefusesForgedTokens(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollNamedMachine(t, e, 71, "alpha", "m-alpha", false)
	enrollNamedMachine(t, e, 72, "beta", "m-beta", false)

	first := listMachines(t, e, &hubv1.ListMachinesRequest{PageSize: 1})
	token := first.GetNextPageToken()
	if token == "" {
		t.Fatal("no next_page_token for a two-row list at page size 1")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	var minted map[string]any
	if err := json.Unmarshal(raw, &minted); err != nil {
		t.Fatal(err)
	}
	minted["key"] = []string{}
	short, err := json.Marshal(minted)
	if err != nil {
		t.Fatal(err)
	}
	forged := base64.RawURLEncoding.EncodeToString(short)
	if _, err := e.client.ListMachines(context.Background(), connect.NewRequest(
		&hubv1.ListMachinesRequest{PageSize: 1, PageToken: forged})); err == nil {
		t.Error("a short-key token succeeded, want PAGE_TOKEN_INVALID")
	} else if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("short key: code %s reason %s, want INVALID_ARGUMENT/PAGE_TOKEN_INVALID", code, reason)
	}

	huge := strings.Repeat("A", 5000)
	if _, err := e.client.ListMachines(context.Background(), connect.NewRequest(
		&hubv1.ListMachinesRequest{PageToken: huge})); err == nil {
		t.Error("an oversized token succeeded, want PAGE_TOKEN_INVALID")
	} else if code, reason, _ := detail(t, err); code != connect.CodeInvalidArgument ||
		reason != typev1.ReasonCode_REASON_CODE_PAGE_TOKEN_INVALID {
		t.Errorf("huge token: code %s reason %s, want INVALID_ARGUMENT/PAGE_TOKEN_INVALID", code, reason)
	}
}

// TestGetMachineServesStoredSnapshotStatus covers what a Snapshot wrote
// after enroll: the resource sample, the policy row and the event counters.
// The setup writes the Snapshot-owned columns straight into the row (the
// Snapshot transaction owns them); the read path under test is production.
func TestGetMachineServesStoredSnapshotStatus(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	enrollNamedMachine(t, e, 81, "alpha", "m-alpha", false)

	resources := `{"load1":1.5,"load5":0.5,"mem_available_bytes":123456,` +
		`"mem_pressure_level":2,"disk":[{"path":"/","free_bytes":100,"total_bytes":200}]}`
	err := e.st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE machines SET resources = ?,
			policy_hash = 'deadbeef', policy_changed_since_load = 1, policy_safe_mode = 1,
			policy_error = 'demo refused', events_shed_total = 7, events_coalesced_total = 8,
			events_evicted_total = 9, last_seen_time = created_time WHERE machine_id = 'm-alpha'`,
			resources)
		return err
	})
	if err != nil {
		t.Fatalf("seed snapshot columns: %v", err)
	}

	resp, err := e.client.GetMachine(context.Background(), connect.NewRequest(
		&hubv1.GetMachineRequest{Id: &typev1.MachineId{Value: "m-alpha"}}))
	if err != nil {
		t.Fatalf("GetMachine: %v", err)
	}
	st := resp.Msg.GetMachine().GetStatus()
	if got := st.GetPolicyHash(); got != "deadbeef" {
		t.Errorf("policy_hash %q, want the stored snapshot policy", got)
	}
	if !st.GetPolicyChangedSinceLoad() || !st.GetPolicySafeMode() {
		t.Errorf("policy changed %v safe %v, want the stored snapshot flags",
			st.GetPolicyChangedSinceLoad(), st.GetPolicySafeMode())
	}
	if got := st.GetPolicyError(); got != "demo refused" {
		t.Errorf("policy_error %q, want the stored snapshot error", got)
	}
	tel := st.GetTelemetry()
	if tel == nil {
		t.Fatal("telemetry is nil, want the stored sample")
	}
	if tel.GetLoad1() != 1.5 || tel.GetLoad5() != 0.5 {
		t.Errorf("load %v/%v, want 1.5/0.5 from the stored sample", tel.GetLoad1(), tel.GetLoad5())
	}
	if tel.GetMemAvailableBytes() != 123456 || tel.GetMemoryPressureLevel() != 2 {
		t.Errorf("mem %d pressure %d, want 123456/2 from the stored sample",
			tel.GetMemAvailableBytes(), tel.GetMemoryPressureLevel())
	}
	disks := tel.GetDisk()
	if len(disks) != 1 || disks[0].GetPath() != "/" ||
		disks[0].GetFreeBytes() != 100 || disks[0].GetTotalBytes() != 200 {
		t.Errorf("disk %v, want [{/ 100 200}] from the stored sample", disks)
	}
	health := tel.GetAgentHealth()
	if health == nil {
		t.Fatal("agent_health is nil, want the stored counters")
	}
	if health.GetEventsShedTotal() != 7 || health.GetEventsCoalescedTotal() != 8 ||
		health.GetEventsEvictedTotal() != 9 {
		t.Errorf("event counters %d/%d/%d, want 7/8/9 from the stored snapshot",
			health.GetEventsShedTotal(), health.GetEventsCoalescedTotal(), health.GetEventsEvictedTotal())
	}
	if !tel.GetLastSeenTime().IsValid() {
		t.Error("last_seen_time unset, want the stored heartbeat")
	}
}
