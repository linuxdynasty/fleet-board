package api_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
)

func (e *quotaWriteEnv) writeCreateRepo(requestID, path string) error {
	req := connect.NewRequest(&hubv1.CreateRepoRequest{
		RequestId: requestID, Repo: repoRequest(requestID, "github.com", path),
	})
	req.Header().Set(api.SecretHeader, e.secret)
	_, err := e.tcp.CreateRepo(context.Background(), req)
	return err
}

func (e *quotaWriteEnv) writeUpdateRepo(requestID, path string, version uint64) error {
	req := connect.NewRequest(&hubv1.UpdateRepoRequest{
		RequestId: requestID, ExpectedVersion: version,
		Repo: &hubv1.Repo{Spec: &hubv1.RepoSpec{
			Repo: &typev1.RepoRef{Host: "github.com", Path: path}, Enabled: false,
		}},
		UpdateMask: fieldMask([]string{"spec.enabled"}),
	})
	req.Header().Set(api.SecretHeader, e.secret)
	_, err := e.tcp.UpdateRepo(context.Background(), req)
	return err
}

func (e *quotaWriteEnv) writeDeleteRepo(requestID, path string, version uint64) error {
	req := connect.NewRequest(&hubv1.DeleteRepoRequest{
		RequestId: requestID, ExpectedVersion: version,
		Id: &hubv1.RepoId{Value: "github.com/" + path},
	})
	req.Header().Set(api.SecretHeader, e.secret)
	_, err := e.tcp.DeleteRepo(context.Background(), req)
	return err
}

func TestLocalWriteQuotaRepoCreate(t *testing.T) {
	e := newQuotaWriteEnv(t, 1)
	seedGitHub(t, e.fleetEnv)
	if err := e.writeCreateRepo("first", "acme/api"); err != nil {
		t.Fatalf("first repo: %v", err)
	}
	wantQuotaCode(t, e.writeCreateRepo("second", "acme/web"), connect.CodeResourceExhausted,
		typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	if got := quotaReceiptCount(t, e, "repo", "second"); got != 0 {
		t.Errorf("refused create receipt count = %d, want 0", got)
	}
	wantQuotaCode(t, e.writeCreateRepo("first", "acme/api"), connect.CodeResourceExhausted,
		typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
	wantQuotaCode(t, e.writeCreateRepo("first", "acme/other"), connect.CodeAlreadyExists,
		typev1.ReasonCode_REASON_CODE_REQUEST_ID_CONFLICT)
	wantQuotaCode(t, e.writeCreateRepo("duplicate", "acme/api"), connect.CodeAlreadyExists,
		typev1.ReasonCode_REASON_CODE_RESOURCE_ALREADY_EXISTS)
	if got := len(e.auditRowText(t, "repo.create")); got != 1 {
		t.Errorf("repo.create audit rows = %d, want 1", got)
	}
}

func TestLocalWriteQuotaRepoUpdateAndDelete(t *testing.T) {
	for _, tc := range []struct {
		name   string
		invoke func(*quotaWriteEnv, string, string, uint64) error
		action string
	}{
		{"update", (*quotaWriteEnv).writeUpdateRepo, "repo.update"},
		{"delete", (*quotaWriteEnv).writeDeleteRepo, "repo.delete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newQuotaWriteEnv(t, 1)
			seedGitHub(t, e.fleetEnv)
			createWatchedRepo(t, e.fleetEnv, "setup-1", "github.com", "acme/api")
			createWatchedRepo(t, e.fleetEnv, "setup-2", "github.com", "acme/web")
			if err := tc.invoke(e, "first", "acme/api", 1); err != nil {
				t.Fatalf("first %s: %v", tc.name, err)
			}
			wantQuotaCode(t, tc.invoke(e, "second", "acme/web", 1), connect.CodeResourceExhausted,
				typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
			if got := quotaReceiptCount(t, e, "repo", "second"); got != 0 {
				t.Errorf("refused %s receipt count = %d, want 0", tc.name, got)
			}
			wantQuotaCode(t, tc.invoke(e, "first", "acme/api", 1), connect.CodeResourceExhausted,
				typev1.ReasonCode_REASON_CODE_RATE_LIMITED)
			wantQuotaCode(t, tc.invoke(e, "missing", "acme/missing", 0), connect.CodeNotFound,
				typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND)
			wantQuotaCode(t, tc.invoke(e, "stale", "acme/web", 9), connect.CodeFailedPrecondition,
				typev1.ReasonCode_REASON_CODE_VERSION_MISMATCH)
			if got := len(e.auditRowText(t, tc.action)); got != 1 {
				t.Errorf("%s audit rows = %d, want 1", tc.action, got)
			}
		})
	}
}
