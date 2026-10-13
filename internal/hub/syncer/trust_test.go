package syncer

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

var (
	trustBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	trustNow  = trustBase.Add(2 * time.Hour)
)

func trustPerson(login, id string) *integrationv1.Person {
	return &integrationv1.Person{Login: login, ProviderId: id}
}

func trustUser(integration, login, id string) *hubv1.TrustedUser {
	return &hubv1.TrustedUser{
		Integration:    &typev1.IntegrationId{Value: integration},
		Login:          login,
		ProviderUserId: id,
	}
}

func trustGate(integration string, users ...*hubv1.TrustedUser) *hubv1.TrustedGate {
	return &hubv1.TrustedGate{Users: users}
}

func trustItem(author *integrationv1.Person, labels ...*integrationv1.Label) *integrationv1.WorkItem {
	return &integrationv1.WorkItem{
		Author:     author,
		Labels:     labels,
		State:      integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN,
		CreateTime: timestamppb.New(trustBase),
	}
}

func trustLabel(name string, by *integrationv1.Person, at time.Time) *integrationv1.Label {
	return &integrationv1.Label{Name: name, AppliedBy: by, AppliedTime: timestamppb.New(at)}
}

func trustInput(item *integrationv1.WorkItem, gate *hubv1.TrustedGate) GateInput {
	return GateInput{
		Item:            item,
		Gate:            gate,
		Integration:     "github-1",
		LabelsWithActor: true,
		EditEvidence:    true,
		Now:             trustNow,
	}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	alice := trustPerson("alice", "1001")
	bob := trustPerson("bob", "1002")

	labelAt := trustBase.Add(time.Hour)
	editAt := trustBase.Add(90 * time.Minute)
	relabelAt := trustBase.Add(100 * time.Minute)

	cases := []struct {
		name       string
		item       *integrationv1.WorkItem
		gate       *hubv1.TrustedGate
		input      func(GateInput) GateInput
		wantTrust  bool
		wantBasis  hubv1.TrustBasis
		wantDenial hubv1.TrustDenial
	}{
		{
			name:      "author on the list is trusted",
			item:      trustItem(alice),
			gate:      trustGate("github-1", trustUser("github-1", "alice", "")),
			wantTrust: true,
			wantBasis: hubv1.TrustBasis_TRUST_BASIS_AUTHOR,
		},
		{
			name:      "login matches case-insensitively",
			item:      trustItem(trustPerson("Alice", "1001")),
			gate:      trustGate("github-1", trustUser("github-1", "ALICE", "")),
			wantTrust: true,
			wantBasis: hubv1.TrustBasis_TRUST_BASIS_AUTHOR,
		},
		{
			name: "label applied by the list is trusted",
			item: trustItem(bob,
				trustLabel("agent-ready", alice, labelAt)),
			gate:      trustGate("github-1", trustUser("github-1", "alice", "")),
			wantTrust: true,
			wantBasis: hubv1.TrustBasis_TRUST_BASIS_LABEL,
		},
		{
			name: "label applied by an outsider is not trusted",
			item: trustItem(bob,
				trustLabel("agent-ready", bob, labelAt)),
			gate:       trustGate("github-1", trustUser("github-1", "alice", "")),
			wantTrust:  false,
			wantDenial: hubv1.TrustDenial_TRUST_DENIAL_NOT_ALLOW_LISTED,
		},
		{
			name: "label with unknown actor fails closed",
			item: trustItem(bob,
				trustLabel("agent-ready", nil, labelAt)),
			gate:       trustGate("github-1", trustUser("github-1", "alice", "")),
			wantTrust:  false,
			wantDenial: hubv1.TrustDenial_TRUST_DENIAL_EVIDENCE_MISSING,
		},
		{
			name: "outsider edit after trust demotes",
			item: func() *integrationv1.WorkItem {
				it := trustItem(alice)
				it.LastEditor = bob
				it.LastEditTime = timestamppb.New(editAt)
				return it
			}(),
			gate:       trustGate("github-1", trustUser("github-1", "alice", "")),
			wantTrust:  false,
			wantDenial: hubv1.TrustDenial_TRUST_DENIAL_EDITED_AFTER_TRUST,
		},
		{
			name: "relabel after the edit restores trust",
			item: func() *integrationv1.WorkItem {
				it := trustItem(alice, trustLabel("agent-ready", alice, relabelAt))
				it.LastEditor = bob
				it.LastEditTime = timestamppb.New(editAt)
				return it
			}(),
			gate:      trustGate("github-1", trustUser("github-1", "alice", "")),
			wantTrust: true,
			wantBasis: hubv1.TrustBasis_TRUST_BASIS_LABEL,
		},
		{
			name:       "recycled login with pinned id does not match",
			item:       trustItem(trustPerson("alice", "9999")),
			gate:       trustGate("github-1", trustUser("github-1", "alice", "1001")),
			wantTrust:  false,
			wantDenial: hubv1.TrustDenial_TRUST_DENIAL_NOT_ALLOW_LISTED,
		},
		{
			name:       "entry for another integration does not apply",
			item:       trustItem(alice),
			gate:       trustGate("jira-1", trustUser("jira-1", "alice", "")),
			wantTrust:  false,
			wantDenial: hubv1.TrustDenial_TRUST_DENIAL_NOT_ALLOW_LISTED,
		},
		{
			name:       "empty list trusts nothing",
			item:       trustItem(alice),
			gate:       trustGate("github-1"),
			wantTrust:  false,
			wantDenial: hubv1.TrustDenial_TRUST_DENIAL_NOT_ALLOW_LISTED,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := trustInput(tc.item, tc.gate)
			if tc.input != nil {
				in = tc.input(in)
			}
			got := Evaluate(in)
			if got.GetTrusted() != tc.wantTrust {
				t.Fatalf("trusted = %v, want %v (denial %v)", got.GetTrusted(), tc.wantTrust, got.GetDenial())
			}
			if tc.wantTrust && got.GetBasis() != tc.wantBasis {
				t.Errorf("basis = %v, want %v", got.GetBasis(), tc.wantBasis)
			}
			if !tc.wantTrust && got.GetDenial() != tc.wantDenial {
				t.Errorf("denial = %v, want %v", got.GetDenial(), tc.wantDenial)
			}
			if got.GetEvaluatedTime() == nil {
				t.Error("evaluated_time is unset")
			}
		})
	}
}

func TestEvaluateNeedsEvidence(t *testing.T) {
	t.Parallel()
	alice := trustPerson("alice", "1001")
	gate := trustGate("github-1", trustUser("github-1", "alice", ""))

	noEdit := Evaluate(GateInput{
		Item: trustItem(alice), Gate: gate, Integration: "github-1",
		LabelsWithActor: true, EditEvidence: false, Now: trustNow,
	})
	if noEdit.GetTrusted() || noEdit.GetDenial() != hubv1.TrustDenial_TRUST_DENIAL_EVIDENCE_MISSING {
		t.Errorf("without edit evidence: trusted=%v denial=%v, want untrusted EVIDENCE_MISSING",
			noEdit.GetTrusted(), noEdit.GetDenial())
	}

	labelled := trustItem(trustPerson("bob", "1002"),
		trustLabel("agent-ready", alice, trustBase.Add(time.Hour)))
	noActor := Evaluate(GateInput{
		Item: labelled, Gate: gate, Integration: "github-1",
		LabelsWithActor: false, EditEvidence: true, Now: trustNow,
	})
	if noActor.GetTrusted() || noActor.GetDenial() != hubv1.TrustDenial_TRUST_DENIAL_EVIDENCE_MISSING {
		t.Errorf("without label actors: trusted=%v denial=%v, want untrusted EVIDENCE_MISSING",
			noActor.GetTrusted(), noActor.GetDenial())
	}

	authorOnly := Evaluate(GateInput{
		Item: trustItem(alice), Gate: gate, Integration: "github-1",
		LabelsWithActor: false, EditEvidence: true, Now: trustNow,
	})
	if !authorOnly.GetTrusted() {
		t.Errorf("author path needs no label actors: trusted=%v denial=%v, want trusted",
			authorOnly.GetTrusted(), authorOnly.GetDenial())
	}
}

func TestInitialStage(t *testing.T) {
	t.Parallel()
	trusted := &hubv1.TrustVerdict{Trusted: true, Basis: hubv1.TrustBasis_TRUST_BASIS_AUTHOR}
	untrusted := &hubv1.TrustVerdict{Trusted: false, Denial: hubv1.TrustDenial_TRUST_DENIAL_NOT_ALLOW_LISTED}
	edited := &hubv1.TrustVerdict{Trusted: false, Denial: hubv1.TrustDenial_TRUST_DENIAL_EDITED_AFTER_TRUST}

	open := integrationv1.WorkItemState_WORK_ITEM_STATE_OPEN
	closed := integrationv1.WorkItemState_WORK_ITEM_STATE_CLOSED

	cases := []struct {
		name        string
		state       integrationv1.WorkItemState
		verdict     *hubv1.TrustVerdict
		claim       bool
		repoEnabled bool
		wantStage   hubv1.WorkStage
		wantReason  hubv1.WorkManualReason
	}{
		{"trusted open item is ready", open, trusted, false, true, hubv1.WorkStage_WORK_STAGE_READY, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{"untrusted open item is manual", open, untrusted, false, true, hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNTRUSTED},
		{"edited after trust is manual with its reason", open, edited, false, true, hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY, hubv1.WorkManualReason_WORK_MANUAL_REASON_EDITED_AFTER_TRUST},
		{"foreign claim wins over ready", open, trusted, true, true, hubv1.WorkStage_WORK_STAGE_CLAIMED_ELSEWHERE, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{"disabled repo stays seen", open, trusted, false, false, hubv1.WorkStage_WORK_STAGE_SEEN, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{"closed item closes whatever the verdict", closed, trusted, false, true, hubv1.WorkStage_WORK_STAGE_CLOSED, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
		{"closed untrusted item closes", closed, untrusted, false, true, hubv1.WorkStage_WORK_STAGE_CLOSED, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stage, reason := InitialStage(tc.state, tc.verdict, tc.claim, tc.repoEnabled)
			if stage != tc.wantStage || reason != tc.wantReason {
				t.Errorf("stage,reason = %v,%v, want %v,%v", stage, reason, tc.wantStage, tc.wantReason)
			}
		})
	}
}
