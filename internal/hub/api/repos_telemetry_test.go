package api_test

import (
	"bufio"
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// repoSample is the value of the repo_writes series with this op and
// outcome, or -1 when absent. sample() cannot match two labels: the
// exporter sorts otel scope labels between them.
func repoSample(t *testing.T, body, op, outcome string) float64 {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "fleet_hub_repo_writes_total{") &&
			strings.Contains(line, `op="`+op+`"`) &&
			strings.Contains(line, `outcome="`+outcome+`"`) {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				return -1
			}
			return v
		}
	}
	return -1
}

// TestRepoWritesAreCountedByOpAndOutcome proves every repo write is counted
// by op and outcome with its refusal reason logged: a create, its replay, a
// refused validation and a delete.
func TestRepoWritesAreCountedByOpAndOutcome(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	if _, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
		RequestId: "", Repo: repoRequest("", "github.com", "acme/api"),
	})); err == nil {
		t.Fatal("empty request_id accepted")
	}
	if _, err := e.client.DeleteRepo(t.Context(), connect.NewRequest(&hubv1.DeleteRepoRequest{
		RequestId: "req-2", Id: &hubv1.RepoId{Value: "github.com/acme/api"}, ExpectedVersion: 1,
	})); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	for _, want := range []struct {
		op, outcome string
		value       float64
	}{
		{"create", "ok", 1},
		{"create", "replayed", 1},
		{"create", "refused", 1},
		{"delete", "ok", 1},
	} {
		if got := repoSample(t, e.scrape(t), want.op, want.outcome); got != want.value {
			t.Errorf("fleet_hub_repo_writes_total{op=%q outcome=%q} = %v, want %v",
				want.op, want.outcome, got, want.value)
		}
	}
	if n := len(e.logs.events(t, "repo.written")); n != 3 {
		t.Errorf("repo.written events = %d, want 3 (create, replay, delete)", n)
	}
	refused := e.logs.events(t, "repo.write_refused")
	if len(refused) != 1 || refused[0]["reason"] != "REQUEST_ID_MISSING" {
		t.Errorf("repo.write_refused events = %v, want one REQUEST_ID_MISSING", refused)
	}
}

// TestRepoWritesNeedSettingsScope proves repo writes sit behind SETTINGS
// (service.proto, 02d): a non-local principal is refused with SCOPE_MISSING
// naming the SETTINGS scope, and the refusal is counted.
func TestRepoWritesNeedSettingsScope(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, &typev1.Principal{
		Name: "agent", Role: typev1.Role_ROLE_OWNER,
		Kind: typev1.PrincipalKind_PRINCIPAL_KIND_DEVICE,
		Id:   &typev1.PrincipalId{Value: "device:a"},
	}, trusted(false))
	_, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
		RequestId: "r", Repo: repoRequest("r", "github.com", "acme/api"),
	}))
	code, reason, _ := detail(t, err)
	if code != connect.CodePermissionDenied || reason != typev1.ReasonCode_REASON_CODE_SCOPE_MISSING {
		t.Fatalf("= %v/%v, want PERMISSION_DENIED/SCOPE_MISSING", code, reason)
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatal("not a Connect error")
	}
	scope := ""
	for _, d := range ce.Details() {
		if msg, derr := d.Value(); derr == nil {
			if ed, ok := msg.(*typev1.ErrorDetail); ok {
				scope = ed.GetMetadata()["scope"]
			}
		}
	}
	if scope != "SETTINGS" {
		t.Errorf("metadata scope = %q, want SETTINGS", scope)
	}
	if got := repoSample(t, e.scrape(t), "create", "refused"); got != 1 {
		t.Errorf("refused count = %v, want 1", got)
	}
	if n := len(e.logs.events(t, "repo.write_refused")); n != 1 {
		t.Errorf("repo.write_refused events = %d, want 1", n)
	}
}

// TestCreateRepoReplayReturnsOriginalResponse proves the API replays the
// stored response: a create retried after a later update answers version 1
// with the original spec, not the current row.
func TestCreateRepoReplayReturnsOriginalResponse(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	first := createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	updated := updateWatchedRepo(t, e, "req-2", &hubv1.RepoSpec{
		Repo:    &typev1.RepoRef{Host: "github.com", Path: "acme/api"},
		Enabled: false,
	}, []string{"spec.enabled"}, first.GetVersion())
	if updated.GetVersion() != 2 {
		t.Fatalf("update version = %d, want 2", updated.GetVersion())
	}
	again, err := e.client.CreateRepo(t.Context(), connect.NewRequest(&hubv1.CreateRepoRequest{
		RequestId: "req-1", Repo: repoRequest("req-1", "github.com", "acme/api"),
	}))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if again.Msg.GetRepo().GetVersion() != 1 {
		t.Errorf("replay version = %d, want the original 1", again.Msg.GetRepo().GetVersion())
	}
	if !again.Msg.GetRepo().GetSpec().GetEnabled() {
		t.Error("replay spec shows the update; want the original enabled spec")
	}
}

// TestConcurrentMaskedUpdatesKeepBothFields proves the merge runs against
// the transaction-current row: concurrent updates to different fields with
// expected_version 0 all land, none restores a stale field.
func TestConcurrentMaskedUpdatesKeepBothFields(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	seedGitHub(t, e)
	createWatchedRepo(t, e, "req-1", "github.com", "acme/api")
	updates := []struct {
		id    string
		paths []string
		spec  *hubv1.RepoSpec
	}{
		{"req-2", []string{"spec.enabled"}, &hubv1.RepoSpec{
			Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/api"}, Enabled: false,
		}},
		{"req-3", []string{"spec.branch_pattern"}, &hubv1.RepoSpec{
			Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/api"}, BranchPattern: "fleet/{goal}",
		}},
		{"req-4", []string{"spec.base_branch"}, &hubv1.RepoSpec{
			Repo: &typev1.RepoRef{Host: "github.com", Path: "acme/api"}, BaseBranch: "main",
		}},
	}
	var wg sync.WaitGroup
	errs := make([]error, len(updates))
	for i, u := range updates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.client.UpdateRepo(context.Background(), connect.NewRequest(&hubv1.UpdateRepoRequest{
				RequestId: u.id,
				Repo: &hubv1.Repo{
					Id:   &hubv1.RepoId{Value: "github.com/acme/api"},
					Spec: u.spec,
				},
				UpdateMask: fieldMask(u.paths), ExpectedVersion: 0,
			}))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	stored, err := statedb.GetWatchedRepo(context.Background(), e.st, "github.com", "acme/api")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Version != 4 {
		t.Errorf("version = %d, want 4 (three updates applied)", stored.Version)
	}
	var spec hubv1.RepoSpec
	if err := protojson.Unmarshal(stored.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.GetEnabled() {
		t.Error("enabled is true: the spec.enabled update was lost")
	}
	if spec.GetBranchPattern() != "fleet/{goal}" {
		t.Errorf("branch_pattern = %q: the branch update was lost", spec.GetBranchPattern())
	}
	if spec.GetBaseBranch() != "main" {
		t.Errorf("base_branch = %q: the base-branch update was lost", spec.GetBaseBranch())
	}
}
