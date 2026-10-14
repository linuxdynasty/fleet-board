package github

import (
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// AdapterVersion is the declared adapter version, for compatibility
// checks (integration.proto).
const AdapterVersion = "1"

// Describe declares what the GitHub adapter supports, so the hub never
// asks for something it cannot do (service.proto Describe, 03j). GitHub
// is a work tracker and a code host; claims are comment markers;
// conditional polling is the only sync mode in v1 (no webhooks: 04 Q8).
func Describe() *integrationv1.IntegrationCapabilities {
	return &integrationv1.IntegrationCapabilities{
		Provider: typev1.ProviderKind_PROVIDER_KIND_GITHUB,
		Roles: []integrationv1.IntegrationRole{
			integrationv1.IntegrationRole_INTEGRATION_ROLE_WORK_TRACKER,
			integrationv1.IntegrationRole_INTEGRATION_ROLE_CODE_HOST,
		},
		WorkItemKinds: []typev1.WorkItemKind{
			typev1.WorkItemKind_WORK_ITEM_KIND_ISSUE,
			typev1.WorkItemKind_WORK_ITEM_KIND_CHANGE_REQUEST,
		},
		ClaimMechanisms: []integrationv1.ClaimMechanism{
			integrationv1.ClaimMechanism_CLAIM_MECHANISM_COMMENT,
		},
		SyncModes: []integrationv1.SyncMode{
			integrationv1.SyncMode_SYNC_MODE_POLL_CONDITIONAL,
		},
		Checks:          true,
		Reviews:         true,
		MergeDetection:  true,
		CrossLinks:      true,
		Labels:          true,
		AdapterVersion:  AdapterVersion,
		MaxBatchSize:    githubMaxBatchSize,
		ProviderName:    "github",
		LabelsWithActor: true,
		EditEvidence:    true,
	}
}
