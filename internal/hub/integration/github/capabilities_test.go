package github

import (
	"testing"

	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Describe is the adapter's capability declaration (service.proto): the
// hub checks it first and never asks for what it lacks (03j).
func TestDescribe(t *testing.T) {
	t.Parallel()
	got := Describe()
	if got.GetProvider() != typev1.ProviderKind_PROVIDER_KIND_GITHUB {
		t.Errorf("provider = %v, want GITHUB", got.GetProvider())
	}
	has := func(roles []integrationv1.IntegrationRole, want integrationv1.IntegrationRole) bool {
		for _, r := range roles {
			if r == want {
				return true
			}
		}
		return false
	}
	if !has(got.GetRoles(), integrationv1.IntegrationRole_INTEGRATION_ROLE_WORK_TRACKER) ||
		!has(got.GetRoles(), integrationv1.IntegrationRole_INTEGRATION_ROLE_CODE_HOST) {
		t.Errorf("roles = %v, want work tracker and code host", got.GetRoles())
	}
	if got.GetMaxBatchSize() != githubMaxBatchSize {
		t.Errorf("max_batch_size = %d, want %d", got.GetMaxBatchSize(), githubMaxBatchSize)
	}
	// The trusted-issues gate passes only with both evidence kinds (03j):
	// label actors for the label path, edit evidence against outsider edits.
	if !got.GetLabelsWithActor() || !got.GetEditEvidence() {
		t.Error("the gate needs labels_with_actor and edit_evidence")
	}
	if !got.GetChecks() || !got.GetReviews() || !got.GetMergeDetection() {
		t.Error("verification needs checks, reviews and merge detection")
	}
}
