package api_test

import (
	"context"
	"database/sql"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

func importDoc(repos ...*hubv1.Repo) *hubv1.FleetConfig {
	specs := make([]*hubv1.RepoSpec, 0, len(repos))
	for _, r := range repos {
		specs = append(specs, r.GetSpec())
	}
	return &hubv1.FleetConfig{FormatVersion: 1, Repos: specs}
}

// statedbRowCount reads how many rows a table holds, for proving a dry run
// or a refused apply wrote nothing. Only the two named tables are allowed.
func statedbRowCount(t *testing.T, e *fleetEnv, table string) int {
	t.Helper()
	if table != "repos" && table != "audit_log" {
		t.Fatalf("row count of %q is not asserted", table)
	}
	var n int
	if err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), `SELECT count(*) FROM `+table).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func dryRun(t *testing.T, e *fleetEnv, requestID string, doc *hubv1.FleetConfig) *hubv1.ImportConfigResponse {
	t.Helper()
	res, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: requestID, Config: doc, DryRun: true,
	}))
	if err != nil {
		t.Fatalf("ImportConfig dry run: %v", err)
	}
	return res.Msg
}

func TestImportDryRunChangesNothing(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	doc := importDoc(repoRequest("", "github.com", "acme/api"), repoRequest("", "github.com", "acme/web"))
	res := dryRun(t, e, "dry-1", doc)
	if res.GetApplied() {
		t.Error("dry run applied the plan")
	}
	if len(res.GetChanges()) != 2 || res.GetPlanHash() == "" {
		t.Errorf("dry run = %d lines hash %q, want 2 CREATE lines and a hash", len(res.GetChanges()), res.GetPlanHash())
	}
	for _, c := range res.GetChanges() {
		if c.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE {
			t.Errorf("line %q kind = %v, want CREATE", c.GetName(), c.GetKind())
		}
	}
	again := dryRun(t, e, "dry-2", doc)
	if again.GetPlanHash() != res.GetPlanHash() {
		t.Error("same document hashes differently on a second dry run")
	}
	if got := statedbRowCount(t, e, "repos"); got != 0 {
		t.Errorf("%d repos stored by a dry run, want 0", got)
	}
	if got := statedbRowCount(t, e, "audit_log"); got != 0 {
		t.Errorf("%d audit rows from a dry run, want 0", got)
	}
	if got := sample(e.scrape(t), "fleet_hub_config_imports_total", `outcome="planned"`); got != 2 {
		t.Errorf("config imports planned = %v, want 2", got)
	}
}

func TestImportStalePlanHashRefusedBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	doc := importDoc(repoRequest("", "github.com", "acme/api"))
	plan := dryRun(t, e, "dry-1", doc)
	_, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: "apply-1", Config: doc, ExpectedPlanHash: "stale",
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodeFailedPrecondition || reason != typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH {
		t.Fatalf("stale hash = %v/%v, want FAILED_PRECONDITION/VERSION_MISMATCH", code, reason)
	}
	if got := statedbRowCount(t, e, "repos"); got != 0 {
		t.Errorf("%d repos stored by a refused apply, want 0", got)
	}
	if got := statedbRowCount(t, e, "audit_log"); got != 0 {
		t.Errorf("%d audit rows from a refused apply, want 0", got)
	}
	// The refusal wrote nothing, so the real hash still applies.
	res, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: "apply-1", Config: doc, ExpectedPlanHash: plan.GetPlanHash(),
	}))
	if err != nil {
		t.Fatalf("apply with the shown hash: %v", err)
	}
	if !res.Msg.GetApplied() || res.Msg.GetPlanHash() != plan.GetPlanHash() {
		t.Errorf("apply = applied %v hash %q, want applied with %q",
			res.Msg.GetApplied(), res.Msg.GetPlanHash(), plan.GetPlanHash())
	}
}

func TestImportApplyWritesReposAndAudits(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	doc := importDoc(repoRequest("", "github.com", "acme/api"))
	plan := dryRun(t, e, "dry-1", doc)
	res, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: "apply-1", Config: doc, ExpectedPlanHash: plan.GetPlanHash(),
	}))
	if err != nil {
		t.Fatalf("ImportConfig apply: %v", err)
	}
	if !res.Msg.GetApplied() {
		t.Fatal("apply did not apply")
	}
	stored, err := statedb.GetWatchedRepo(context.Background(), e.st, "github.com", "acme/api")
	if err != nil {
		t.Fatalf("GetWatchedRepo: %v", err)
	}
	if stored.Version != 1 {
		t.Errorf("imported version = %d, want 1", stored.Version)
	}
	rows := e.auditRowText(t, "config.import")
	if len(rows) != 1 {
		t.Fatalf("config.import rows = %d, want 1", len(rows))
	}
	if got := sample(e.scrape(t), "fleet_hub_config_imports_total", `outcome="applied"`); got != 1 {
		t.Errorf("config imports applied = %v, want 1", got)
	}
	if n := len(e.logs.events(t, "config.import_applied")); n != 1 {
		t.Errorf("config.import_applied events = %d, want 1", n)
	}
}

func TestImportApplyReplaysAndConflicts(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	doc := importDoc(repoRequest("", "github.com", "acme/api"))
	plan := dryRun(t, e, "dry-1", doc)
	apply := func(requestID string, d *hubv1.FleetConfig) (*hubv1.ImportConfigResponse, error) {
		res, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
			RequestId: requestID, Config: d, ExpectedPlanHash: plan.GetPlanHash(),
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	first, err := apply("apply-1", doc)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second, err := apply("apply-1", doc)
	if err != nil {
		t.Fatalf("replay apply: %v", err)
	}
	if second.GetPlanHash() != first.GetPlanHash() || !second.GetApplied() {
		t.Error("replay did not return the stored response")
	}
	if got := sample(e.scrape(t), "fleet_hub_config_imports_total", `outcome="replayed"`); got != 1 {
		t.Errorf("config imports replayed = %v, want 1", got)
	}
	other := importDoc(repoRequest("", "github.com", "acme/other"))
	otherPlan := dryRun(t, e, "dry-other", other)
	_, err = e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: "apply-1", Config: other, ExpectedPlanHash: otherPlan.GetPlanHash(),
	}))
	if err == nil {
		t.Fatal("request_id reuse with another body accepted")
	} else if code, reason, _ := detail(t, err); code != connect.CodeAlreadyExists ||
		reason != typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT {
		t.Errorf("conflict = %v/%v, want ALREADY_EXISTS/REQUEST_ID_CONFLICT", code, reason)
	}
}

func TestImportValidatesItsEnvelope(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	good := importDoc(repoRequest("", "github.com", "acme/api"))
	cases := []struct {
		name      string
		requestID string
		doc       *hubv1.FleetConfig
		code      connect.Code
		reason    typev1.ReasonCode
		field     string
	}{
		{
			"request id required", "", good,
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id",
		},
		{
			"format version required", "v", &hubv1.FleetConfig{},
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "config.format_version",
		},
		{
			"config required", "v", nil,
			connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "config",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
				RequestId: tc.requestID, Config: tc.doc, DryRun: true,
			}))
			code, reason, fields := detail(t, err)
			if code != tc.code || reason != tc.reason {
				t.Fatalf("= %v/%v, want %v/%v", code, reason, tc.code, tc.reason)
			}
			found := false
			for _, f := range fields {
				if f == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("violations = %v, want %q", fields, tc.field)
			}
		})
	}
	if got := statedbRowCount(t, e, "audit_log"); got != 0 {
		t.Errorf("%d audit rows from refused imports, want 0", got)
	}
}

func TestImportNeedsSettingsScopeAndSecret(t *testing.T) {
	t.Parallel()
	device := newFleetEnv(t, &typev1.Principal{
		Name: "agent", Role: typev1.Role_ROLE_OWNER,
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE,
		Id:   &typev1.PrincipalId{Value: "device:a"},
	}, trusted(false))
	_, err := device.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: "d", Config: importDoc(), DryRun: true,
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodePermissionDenied || reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Fatalf("device import = %v/%v, want PERMISSION_DENIED/SCOPE_MISSING", code, reason)
	}
	nosecret := newFleetEnv(t, localUser("alice"))
	_, err = nosecret.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: "d", Config: importDoc(), DryRun: true,
	}))
	if code, reason, _ := detail(t, err); code != connect.CodePermissionDenied ||
		reason != typev1.ReasonCode_REASON_CODE_CREDENTIALS_INVALID {
		t.Errorf("import without the local secret = %v/%v, want PERMISSION_DENIED/CREDENTIALS_INVALID", code, reason)
	}
}

func TestImportPlansUpdateAndUnchanged(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	same := importDoc(repoRequest("", "github.com", "acme/api"))
	res := dryRun(t, e, "dry-1", same)
	if len(res.GetChanges()) != 1 ||
		res.GetChanges()[0].GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED {
		t.Fatalf("same spec plans %v, want one UNCHANGED line", res.GetChanges())
	}
	changed := repoRequest("", "github.com", "acme/api")
	changed.GetSpec().Enabled = false
	res = dryRun(t, e, "dry-2", importDoc(changed))
	if len(res.GetChanges()) != 1 ||
		res.GetChanges()[0].GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE {
		t.Fatalf("changed spec plans %v, want one UPDATE line", res.GetChanges())
	}
	if fields := res.GetChanges()[0].GetChangedFields(); len(fields) != 1 || fields[0] != "spec.enabled" {
		t.Errorf("changed_fields = %v, want [spec.enabled]", fields)
	}
	applied, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: "apply-1", Config: importDoc(changed), ExpectedPlanHash: res.GetPlanHash(),
	}))
	if err != nil {
		t.Fatalf("apply update: %v", err)
	}
	if !applied.Msg.GetApplied() {
		t.Fatal("update not applied")
	}
	stored, err := statedb.GetWatchedRepo(context.Background(), e.st, "github.com", "acme/api")
	if err != nil {
		t.Fatalf("GetWatchedRepo: %v", err)
	}
	if stored.Version != 2 {
		t.Errorf("imported update version = %d, want 2", stored.Version)
	}
}

func TestImportUnsupportedSectionsBlockApply(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	doc := importDoc(repoRequest("", "github.com", "acme/api"))
	doc.Accounts = []*hubv1.AccountConfig{{Spec: &hubv1.AccountSpec{DisplayName: "codex"}}}
	res := dryRun(t, e, "dry-1", doc)
	if res.GetApplied() {
		t.Error("dry run applied")
	}
	found := false
	for _, c := range res.GetChanges() {
		if c.GetName() == "codex" && c.GetKind() == hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_SKIPPED &&
			c.GetProblem().GetReason() == typev1.ReasonCode_REASON_CODE_CONFIG_INVALID {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan %v names no blocked account line", res.GetChanges())
	}
	// allow_skipped does not waive a validation failure: nothing applies.
	apply, err := e.client.ImportConfig(t.Context(), connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: "apply-1", Config: doc, ExpectedPlanHash: res.GetPlanHash(), AllowSkipped: true,
	}))
	if err != nil {
		t.Fatalf("blocked apply is a plan answer, not an RPC error: %v", err)
	}
	if apply.Msg.GetApplied() {
		t.Error("apply ran with an unbuilt section in the document")
	}
	if got := statedbRowCount(t, e, "repos"); got != 0 {
		t.Errorf("%d repos stored by a blocked apply, want 0", got)
	}
	if got := statedbRowCount(t, e, "audit_log"); got != 0 {
		t.Errorf("%d audit rows from a blocked apply, want 0", got)
	}
}
