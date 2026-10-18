package api_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func TestLocalReadQuotaExportDoesNotReportRefusalAsSuccess(t *testing.T) {
	e := newLocalQuotaEnv(t, 1, 100)
	if _, err := e.tcp.ExportConfig(t.Context(), connect.NewRequest(&hubv1.ExportConfigRequest{})); err != nil {
		t.Fatalf("admitted export: %v", err)
	}
	if got := configSample(t, e.scrape(t), "export", "ok"); got != 1 {
		t.Fatalf("admitted export successes = %v, want 1", got)
	}
	_, err := e.tcp.ExportConfig(t.Context(), connect.NewRequest(&hubv1.ExportConfigRequest{}))
	wantQuotaCode(t, err, connect.CodeResourceExhausted, typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	if got := configSample(t, e.scrape(t), "export", "ok"); got != 1 {
		t.Errorf("successes after quota refusal = %v, want 1", got)
	}
	if got := strings.Count(e.logs.String(), `"msg":"config.exported"`); got != 1 {
		t.Errorf("export success logs after quota refusal = %d, want 1", got)
	}
}
