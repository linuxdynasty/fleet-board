package api_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"testing"
	"time"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// readMachineDrained reads one machine's drain flag for tests that
// prove import and enroll leave it alone or set it.
func readMachineDrained(t *testing.T, e *fleetEnv, id string, drained *int) error {
	t.Helper()
	return e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT drained FROM machines WHERE machine_id = ?`, id).Scan(drained)
	})
}

func applyDoc(t *testing.T, e *fleetEnv, requestID string, doc *hubv1.FleetConfig, hash string) *hubv1.ImportConfigResponse {
	t.Helper()
	res, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: requestID, Config: doc, ExpectedPlanHash: hash,
	}))
	if err != nil {
		t.Fatalf("ImportConfig apply: %v", err)
	}
	return res.Msg
}

func pendingNames(t *testing.T, e *fleetEnv) []statedb.PendingMachine {
	t.Helper()
	rows, err := statedb.ListPendingMachines(context.Background(), e.st)
	if err != nil {
		t.Fatalf("ListPendingMachines: %v", err)
	}
	return rows
}

// TestImportApplyKeepsPendingMachines is criterion 21 over RPC: an entry
// no machine has is kept, exported, and re-imports to unchanged lines.
func TestImportApplyKeepsPendingMachines(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	three := uint32(3)
	doc := &hubv1.FleetConfig{FormatVersion: 1, Machines: []*hubv1.MachineConfig{{
		Name: "field-laptop", Labels: []string{"field"}, Values: &typev1.SettingValues{MaxActive: &three},
	}}}
	plan := dryRun(t, e, "dry-1", doc)
	if len(plan.GetChanges()) != 1 {
		t.Fatalf("plan lines = %+v, want 1", plan.GetChanges())
	}
	got := plan.GetChanges()[0]
	if got.GetEntity() != hubv1.ConfigEntity_CONFIG_ENTITY_PENDING_MACHINE ||
		got.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE {
		t.Fatalf("line = %v/%v, want PENDING_MACHINE CREATE", got.GetEntity(), got.GetKind())
	}
	res := applyDoc(t, e, "apply-1", doc, plan.GetPlanHash())
	if !res.GetApplied() {
		t.Fatal("apply did not apply")
	}
	rows := pendingNames(t, e)
	if len(rows) != 1 || rows[0].Name != "field-laptop" {
		t.Fatalf("pending rows = %+v, want field-laptop", rows)
	}
	if len(rows[0].Labels) != 1 || rows[0].Labels[0] != "field" {
		t.Errorf("pending labels = %q, want [field]", rows[0].Labels)
	}
	exported := exportConfig(t, e)
	if len(exported.GetMachines()) != 1 || exported.GetMachines()[0].GetName() != "field-laptop" {
		t.Fatalf("exported machines = %+v, want field-laptop", exported.GetMachines())
	}
	if exported.GetMachines()[0].GetValues().GetMaxActive() != 3 {
		t.Errorf("exported values = %+v, want max_active 3", exported.GetMachines()[0].GetValues())
	}
	again := dryRun(t, e, "dry-2", exported)
	if len(again.GetChanges()) == 0 {
		t.Fatal("re-import plan has no lines, want the kept entry")
	}
	for _, c := range again.GetChanges() {
		if c.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED {
			t.Errorf("re-import line %+v is not unchanged", c)
		}
	}
}

// TestImportApplySetsLabelsAndValuesOnEnrolledMachine is the matched
// half over RPC: the entry's labels replace the machine's and its
// values become the machine's settings layer, without draining it.
func TestImportApplySetsLabelsAndValuesOnEnrolledMachine(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedMachine(t, e, "01jimportmachine00000001")
	four := uint32(4)
	doc := &hubv1.FleetConfig{FormatVersion: 1, Machines: []*hubv1.MachineConfig{{
		Name: "laptop", Labels: []string{"gpu", "field"}, Values: &typev1.SettingValues{MaxActive: &four},
	}}}
	plan := dryRun(t, e, "dry-1", doc)
	if len(plan.GetChanges()) != 1 || plan.GetChanges()[0].GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE {
		t.Fatalf("plan lines = %+v, want one UPDATE", plan.GetChanges())
	}
	res := applyDoc(t, e, "apply-1", doc, plan.GetPlanHash())
	if !res.GetApplied() {
		t.Fatal("apply did not apply")
	}
	identities, err := statedb.ListMachineIdentities(context.Background(), e.st)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || len(identities[0].Labels) != 2 ||
		identities[0].Labels[0] != "gpu" || identities[0].Labels[1] != "field" {
		t.Errorf("machine labels = %+v, want [gpu field]", identities)
	}
	layer, found, err := statedb.ReadMachineLayer(context.Background(), e.st, "01jimportmachine00000001")
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(layer) != `{"values":{"maxActive":4}}` {
		t.Errorf("machine layer = %q found=%v, want max_active 4", layer, found)
	}
	var drained int
	if err := readMachineDrained(t, e, "01jimportmachine00000001", &drained); err != nil {
		t.Fatal(err)
	}
	if drained != 0 {
		t.Error("import drained an enrolled machine, want the drain flag untouched")
	}
	events := e.logs.events(t, "config.import_applied")
	if len(events) != 1 {
		t.Fatalf("config.import_applied events = %d, want 1", len(events))
	}
	if events[0]["machines"] != float64(1) || events[0]["pending"] != float64(0) {
		t.Errorf("applied event = %v, want machines 1 pending 0", events[0])
	}
	exported := exportConfig(t, e)
	if len(exported.GetMachines()) != 1 || exported.GetMachines()[0].GetValues().GetMaxActive() != 4 {
		t.Errorf("exported machines = %+v, want the laptop with max_active 4", exported.GetMachines())
	}
}

// TestImportPendingAppliedAtEnroll closes the 02m loop over the
// production path: import keeps the entry, and the enroll with a token
// for that name merges its labels in, records the machine drained and
// stores its values as the machine's layer.
func TestImportPendingAppliedAtEnroll(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	three := uint32(3)
	doc := &hubv1.FleetConfig{FormatVersion: 1, Machines: []*hubv1.MachineConfig{{
		Name: "field-laptop", Labels: []string{"field"}, Values: &typev1.SettingValues{MaxActive: &three},
	}}}
	plan := dryRun(t, e, "dry-1", doc)
	applyDoc(t, e, "apply-1", doc, plan.GetPlanHash())
	ctx := context.Background()
	secret := sha256.Sum256([]byte("pending-enroll"))
	tok := statedb.EnrollToken{
		SecretHash: secret, MachineName: "field-laptop", ExpectedHostname: "", Labels: []string{"token"},
		NotAfter: fleetNow.Add(time.Hour), Created: fleetNow,
		Principal: "local:alice", RequestID: "enroll-1", RequestHash: sha256.Sum256([]byte("enroll")),
	}
	if err := statedb.InsertEnrollToken(ctx, e.st, tok); err != nil {
		t.Fatal(err)
	}
	key := statedb.EnrollKey{SecretHash: secret, InstallID: "install-9", CSRKeyHash: sha256.Sum256([]byte("csr-9"))}
	if _, err := statedb.Enroll(ctx, e.st, statedb.NewMachine{
		Key: key, MachineID: "01jpendingmachine000001", CertDER: []byte("cert"), Serial: "ab0001",
		CertNotAfter: fleetNow.Add(90 * 24 * time.Hour), ReportedHostname: "field-laptop",
		Facts: `{"hostname":"field-laptop"}`, Now: fleetNow,
	}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	identities, err := statedb.ListMachineIdentities(ctx, e.st)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || len(identities[0].Labels) != 2 ||
		identities[0].Labels[0] != "field" || identities[0].Labels[1] != "token" {
		t.Errorf("enrolled labels = %+v, want [field token]", identities)
	}
	var drained int
	if err := readMachineDrained(t, e, "01jpendingmachine000001", &drained); err != nil {
		t.Fatal(err)
	}
	if drained != 1 {
		t.Error("enrolled machine is not drained, want drained until UndrainMachine")
	}
	layer, found, err := statedb.ReadMachineLayer(ctx, e.st, "01jpendingmachine000001")
	if err != nil {
		t.Fatal(err)
	}
	if !found || string(layer) != `{"values":{"maxActive":3}}` {
		t.Errorf("machine layer = %q found=%v, want max_active 3", layer, found)
	}
	if rows := pendingNames(t, e); len(rows) != 0 {
		t.Errorf("pending rows after enroll = %+v, want none consumed", rows)
	}
}
