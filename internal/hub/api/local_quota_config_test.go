package api_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
)

func quotaReceiptCount(t *testing.T, e *quotaWriteEnv, table, requestID string) int {
	t.Helper()
	query := ""
	switch table {
	case "config":
		query = `SELECT count(*) FROM config_request_log WHERE request_id = ?`
	case "repo":
		query = `SELECT count(*) FROM repo_request_log WHERE request_id = ?`
	case "import":
		query = `SELECT count(*) FROM import_request_log WHERE request_id = ?`
	default:
		t.Fatalf("unknown receipt table %q", table)
	}
	var count int
	if err := e.st.Read(t.Context(), func(tx *sql.Tx) error {
		return tx.QueryRowContext(t.Context(), query, requestID).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func wantQuotaCode(t *testing.T, err error, code connect.Code, reason typev1.ReasonCode) {
	t.Helper()
	gotCode, gotReason, _ := detail(t, err)
	if gotCode != code || gotReason != reason {
		t.Errorf("result = %v/%v, want %v/%v", gotCode, gotReason, code, reason)
	}
}

func (e *quotaWriteEnv) writeSettings(id string, version uint64, value uint32) error {
	req := connect.NewRequest(&hubv1.UpdateSettingsRequest{
		RequestId: id, ExpectedVersion: version,
		Settings: &hubv1.Settings{Spec: &hubv1.SettingsSpec{
			Scope: fleetScope(), Values: &typev1.SettingValues{MaxActive: &value},
		}},
		UpdateMask: fieldMask([]string{"spec.values.max_active"}),
	})
	req.Header().Set(api.SecretHeader, e.secret)
	_, err := e.tcp.UpdateSettings(context.Background(), req)
	return err
}

func (e *quotaWriteEnv) writeIntegration(requestID, integrationID, label string, version uint64) error {
	req := connect.NewRequest(&hubv1.UpdateIntegrationRequest{
		RequestId: requestID, ExpectedVersion: version,
		Integration: &hubv1.Integration{
			Id:   &typev1.IntegrationId{Value: integrationID},
			Spec: &hubv1.IntegrationSpec{Label: label},
		},
		UpdateMask: fieldMask([]string{"spec.label"}),
	})
	req.Header().Set(api.SecretHeader, e.secret)
	_, err := e.tcp.UpdateIntegration(context.Background(), req)
	return err
}

func TestLocalWriteQuotaSettings(t *testing.T) {
	e := newQuotaWriteEnv(t, 1)
	if err := e.writeSettings("first", 0, 2); err != nil {
		t.Fatalf("first settings write: %v", err)
	}
	wantQuotaCode(t, e.writeSettings("second", 1, 3), connect.CodeResourceExhausted,
		typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	if got := quotaReceiptCount(t, e, "config", "second"); got != 0 {
		t.Errorf("refused settings receipt count = %d, want 0", got)
	}
	wantQuotaCode(t, e.writeSettings("first", 0, 2), connect.CodeResourceExhausted,
		typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	wantQuotaCode(t, e.writeSettings("first", 0, 3), connect.CodeAlreadyExists,
		typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT)
	wantQuotaCode(t, e.writeSettings("stale", 9, 3), connect.CodeFailedPrecondition,
		typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH)
	if got := len(e.auditRowText(t, "settings.update")); got != 1 {
		t.Errorf("settings.update audit rows = %d, want 1", got)
	}
}

func TestLocalWriteQuotaIsSharedAcrossFamilies(t *testing.T) {
	e := newQuotaWriteEnv(t, 1)
	if _, err := e.tcp.CreateAccount(context.Background(), e.protectedCreateAccount("account", "work")); err != nil {
		t.Fatalf("account write: %v", err)
	}
	wantQuotaCode(t, e.writeSettings("settings", 0, 2), connect.CodeResourceExhausted,
		typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	if got := len(e.auditRowText(t, "settings.update")); got != 0 {
		t.Errorf("settings.update audit rows = %d, want 0", got)
	}
}

func TestLocalWriteQuotaIntegration(t *testing.T) {
	e := newQuotaWriteEnv(t, 1)
	seedGitHub(t, e.fleetEnv)
	if err := e.writeIntegration("first", "github", "GitHub", 1); err != nil {
		t.Fatalf("first integration write: %v", err)
	}
	wantQuotaCode(t, e.writeIntegration("second", "github", "New", 2), connect.CodeResourceExhausted,
		typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	if got := quotaReceiptCount(t, e, "config", "second"); got != 0 {
		t.Errorf("refused integration receipt count = %d, want 0", got)
	}
	wantQuotaCode(t, e.writeIntegration("first", "github", "GitHub", 1), connect.CodeResourceExhausted,
		typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	wantQuotaCode(t, e.writeIntegration("first", "github", "New", 1), connect.CodeAlreadyExists,
		typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT)
	wantQuotaCode(t, e.writeIntegration("missing", "missing", "New", 0), connect.CodeNotFound,
		typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND)
	wantQuotaCode(t, e.writeIntegration("stale", "github", "New", 9), connect.CodeFailedPrecondition,
		typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH)
	if got := len(e.auditRowText(t, "integration.update")); got != 1 {
		t.Errorf("integration.update audit rows = %d, want 1", got)
	}
}

func (e *quotaWriteEnv) writeImport(id string, doc *hubv1.FleetConfig, dry bool, hash string) (*hubv1.ImportConfigResponse, error) {
	req := connect.NewRequest(&hubv1.ImportConfigRequest{
		RequestId: id, Config: doc, DryRun: dry, ExpectedPlanHash: hash,
	})
	req.Header().Set(api.SecretHeader, e.secret)
	response, err := e.tcp.ImportConfig(context.Background(), req)
	if err != nil {
		return nil, err
	}
	return response.Msg, nil
}

func TestLocalWriteQuotaImportDryRunAndBlockedPlan(t *testing.T) {
	t.Run("dry run", func(t *testing.T) {
		e := newQuotaWriteEnv(t, 1)
		doc := &hubv1.FleetConfig{FormatVersion: 1}
		if _, err := e.writeImport("first", doc, true, ""); err != nil {
			t.Fatalf("first dry run: %v", err)
		}
		_, err := e.writeImport("second", doc, true, "")
		wantQuotaCode(t, err, connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
		_, err = e.writeImport("invalid", &hubv1.FleetConfig{FormatVersion: 9}, true, "")
		wantQuotaCode(t, err, connect.CodeInvalidArgument, typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT)
		if got := len(e.auditRowText(t, "config.import")); got != 0 {
			t.Errorf("config.import audit rows = %d, want 0", got)
		}
	})
	t.Run("blocked plan", func(t *testing.T) {
		e := newQuotaWriteEnv(t, 1)
		doc := importDoc(repoRequest("", "unknown.example", "acme/api"))
		plan := dryRun(t, e.fleetEnv, "plan", doc)
		first, err := e.writeImport("blocked", doc, false, plan.GetPlanHash())
		if err != nil || first.GetApplied() {
			t.Fatalf("blocked import = %+v/%v, want applied=false success", first, err)
		}
		_, err = e.writeImport("next", &hubv1.FleetConfig{FormatVersion: 1}, true, "")
		wantQuotaCode(t, err, connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
		if got := len(e.auditRowText(t, "config.import")); got != 0 {
			t.Errorf("config.import audit rows = %d, want 0", got)
		}
	})
}

func TestLocalWriteQuotaImportApplyReplayAndAtomicRefusal(t *testing.T) {
	t.Run("apply and replay", func(t *testing.T) {
		e := newQuotaWriteEnv(t, 2)
		seedGitHub(t, e.fleetEnv)
		doc := importDoc(repoRequest("", "github.com", "acme/api"))
		plan := dryRun(t, e.fleetEnv, "plan", doc)
		first, err := e.writeImport("apply", doc, false, plan.GetPlanHash())
		if err != nil || !first.GetApplied() {
			t.Fatalf("apply = %+v/%v, want applied success", first, err)
		}
		if got := quotaReceiptCount(t, e, "import", "apply"); got != 1 {
			t.Fatalf("admitted import receipt count = %d, want 1", got)
		}
		replay, err := e.writeImport("apply", doc, false, plan.GetPlanHash())
		if err != nil || !replay.GetApplied() || replay.GetPlanHash() != first.GetPlanHash() {
			t.Fatalf("replay = %+v/%v, want stored applied result", replay, err)
		}
		_, err = e.writeImport("apply", doc, false, plan.GetPlanHash())
		wantQuotaCode(t, err, connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
		if got := quotaReceiptCount(t, e, "import", "apply"); got != 1 {
			t.Errorf("replayed import receipt count = %d, want 1", got)
		}
		_, err = e.writeImport("stale", doc, false, plan.GetPlanHash())
		wantQuotaCode(t, err, connect.CodeFailedPrecondition, typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH)
		if got := len(e.auditRowText(t, "config.import")); got != 1 {
			t.Errorf("config.import audit rows = %d, want 1", got)
		}
	})
	t.Run("refused plan is all or nothing", func(t *testing.T) {
		e := newQuotaWriteEnv(t, 1)
		seedGitHub(t, e.fleetEnv)
		doc := importDoc(repoRequest("", "github.com", "acme/api"), repoRequest("", "github.com", "acme/web"))
		plan, err := e.writeImport("plan", doc, true, "")
		if err != nil {
			t.Fatalf("dry run: %v", err)
		}
		_, err = e.writeImport("apply", doc, false, plan.GetPlanHash())
		wantQuotaCode(t, err, connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
		if got := quotaReceiptCount(t, e, "import", "apply"); got != 0 {
			t.Errorf("refused import receipt count = %d, want 0", got)
		}
		if got := statedbRowCount(t, e.fleetEnv, "repos"); got != 0 {
			t.Errorf("repos after refused apply = %d, want 0", got)
		}
		if got := len(e.auditRowText(t, "config.import")); got != 0 {
			t.Errorf("config.import audit rows = %d, want 0", got)
		}
	})
	t.Run("no-op apply", func(t *testing.T) {
		e := newQuotaWriteEnv(t, 1)
		doc := &hubv1.FleetConfig{FormatVersion: 1}
		plan := dryRun(t, e.fleetEnv, "plan", doc)
		result, err := e.writeImport("apply", doc, false, plan.GetPlanHash())
		if err != nil || !result.GetApplied() || len(result.GetChanges()) != 0 {
			t.Fatalf("no-op apply = %+v/%v, want applied with no changes", result, err)
		}
		_, err = e.writeImport("next", doc, true, "")
		wantQuotaCode(t, err, connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
		if got := len(e.auditRowText(t, "config.import")); got != 1 {
			t.Errorf("no-op audit rows = %d, want 1", got)
		}
	})
}

func TestLocalWriteQuotaConcurrentImportReplay(t *testing.T) {
	e := newQuotaWriteEnv(t, 2)
	seedGitHub(t, e.fleetEnv)
	doc := importDoc(repoRequest("", "github.com", "acme/api"))
	plan := dryRun(t, e.fleetEnv, "plan", doc)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.writeImport("apply", doc, false, plan.GetPlanHash())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent apply: %v", err)
		}
	}
	_, err := e.writeImport("apply", doc, false, plan.GetPlanHash())
	wantQuotaCode(t, err, connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	if got := statedbRowCount(t, e.fleetEnv, "repos"); got != 1 {
		t.Errorf("repos = %d, want one applied row", got)
	}
	if got := len(e.auditRowText(t, "config.import")); got != 1 {
		t.Errorf("config.import audit rows = %d, want 1", got)
	}
}
